package sync

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/prysmaticlabs/go-bitfield"
	mockChain "github.com/prysmaticlabs/prysm/v3/beacon-chain/blockchain/testing"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/altair"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed"
	opfeed "github.com/prysmaticlabs/prysm/v3/beacon-chain/core/feed/operation"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/signing"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/core/transition"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/db"
	testingdb "github.com/prysmaticlabs/prysm/v3/beacon-chain/db/testing"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/p2p"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/p2p/encoder"
	mockp2p "github.com/prysmaticlabs/prysm/v3/beacon-chain/p2p/testing"
	p2ptypes "github.com/prysmaticlabs/prysm/v3/beacon-chain/p2p/types"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state/stategen"
	mockSync "github.com/prysmaticlabs/prysm/v3/beacon-chain/sync/initial-sync/testing"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/blocks"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/crypto/bls"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/v3/testing/assert"
	"github.com/prysmaticlabs/prysm/v3/testing/require"
	"github.com/prysmaticlabs/prysm/v3/testing/util"
	"github.com/prysmaticlabs/prysm/v3/time/slots"
)

func TestService_ValidateSyncContributionAndProof(t *testing.T) {
	database := testingdb.SetupDB(t)
	headRoot, keys := fillUpBlocksAndState(context.Background(), t, database)
	defaultTopic := p2p.SyncContributionAndProofSubnetTopicFormat
	defaultTopic = fmt.Sprintf(defaultTopic, []byte{0xAB, 0x00, 0xCC, 0x9E})
	defaultTopic = defaultTopic + "/" + encoder.ProtocolSuffixSSZSnappy
	chainService := &mockChain.ChainService{
		Genesis:        time.Now(),
		ValidatorsRoot: [32]byte{'A'},
	}
	emptySig := [96]byte{}
	type args struct {
		ctx   context.Context
		pid   peer.ID
		msg   *ethpb.SignedContributionAndProof
		topic string
	}
	tests := []struct {
		name     string
		svc      *Service
		setupSvc func(s *Service, msg *ethpb.SignedContributionAndProof) *Service
		args     args
		want     pubsub.ValidationResult
	}{
		{
			name: "Is syncing",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: true}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				msg.Message.Contribution.BlockRoot = headRoot[:]
				s.cfg.beaconDB = database
				s.initCaches()
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: "junk",
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationIgnore,
		},
		{
			name: "Bad Topic",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				msg.Message.Contribution.BlockRoot = headRoot[:]
				s.cfg.beaconDB = database
				s.initCaches()
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: "junk",
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationReject,
		},
		{
			name: "Future Slot Message",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				s.cfg.beaconDB = database
				s.initCaches()
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              30,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationIgnore,
		},
		{
			name: "Already Seen Message",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				s.cfg.beaconDB = database
				s.initCaches()
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot: [32]byte{'A'},
					Genesis:        time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
				}
				msg.Message.Contribution.BlockRoot = headRoot[:]
				msg.Message.Contribution.AggregationBits.SetBitAt(1, true)

				s.setSyncContributionIndexSlotSeen(1, 1, 1)
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationIgnore,
		},
		{
			name: "Invalid Subcommittee Index",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				s.cfg.beaconDB = database
				s.initCaches()
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot: [32]byte{'A'},
					Genesis:        time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
				}
				msg.Message.Contribution.BlockRoot = headRoot[:]
				msg.Message.Contribution.AggregationBits.SetBitAt(1, true)
				msg.Message.Contribution.SubcommitteeIndex = 20

				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationReject,
		},
		{
			name: "Invalid Selection Proof",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				s.cfg.beaconDB = database
				s.initCaches()
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot: [32]byte{'A'},
					Genesis:        time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
				}
				msg.Message.Contribution.BlockRoot = headRoot[:]
				incorrectProof := [96]byte{0xBB}
				msg.Message.SelectionProof = incorrectProof[:]
				msg.Message.Contribution.AggregationBits.SetBitAt(1, true)

				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationReject,
		},
		{
			name: "Invalid Aggregator",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				s.cfg.beaconDB = database
				s.initCaches()
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot: [32]byte{'A'},
					Genesis:        time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
				}
				msg.Message.Contribution.BlockRoot = headRoot[:]
				hState, err := database.State(context.Background(), headRoot)
				assert.NoError(t, err)
				sc, err := hState.CurrentSyncCommittee()
				assert.NoError(t, err)
				for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
					coms, err := altair.SyncSubCommitteePubkeys(sc, types.CommitteeIndex(i))
					assert.NoError(t, err)
					for _, p := range coms {
						idx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(p))
						assert.Equal(t, true, ok)
						rt, err := syncSelectionProofSigningRoot(hState, slots.PrevSlot(hState.Slot()), types.CommitteeIndex(i))
						assert.NoError(t, err)
						sig := keys[idx].Sign(rt[:])
						isAggregator, err := altair.IsSyncCommitteeAggregator(sig.Marshal())
						require.NoError(t, err)
						if !isAggregator {
							msg.Message.AggregatorIndex = idx
							break
						}
					}
				}
				msg.Message.Contribution.AggregationBits.SetBitAt(1, true)
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationReject,
		},
		{
			name: "Failed Selection Proof Verification ",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				s.cfg.beaconDB = database
				msg.Message.Contribution.BlockRoot = headRoot[:]
				hState, err := database.State(context.Background(), headRoot)
				assert.NoError(t, err)
				sc, err := hState.CurrentSyncCommittee()
				assert.NoError(t, err)
				for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
					coms, err := altair.SyncSubCommitteePubkeys(sc, types.CommitteeIndex(i))
					assert.NoError(t, err)
					for _, p := range coms {
						idx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(p))
						assert.Equal(t, true, ok)
						rt, err := syncSelectionProofSigningRoot(hState, slots.PrevSlot(hState.Slot()), types.CommitteeIndex(i))
						assert.NoError(t, err)
						sig := keys[idx].Sign(rt[:])
						isAggregator, err := altair.IsSyncCommitteeAggregator(sig.Marshal())
						require.NoError(t, err)
						if !isAggregator {
							msg.Message.AggregatorIndex = idx
							break
						}
					}
				}
				subCommitteeSize := params.BeaconConfig().SyncCommitteeSize / params.BeaconConfig().SyncCommitteeSubnetCount
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot:       [32]byte{'A'},
					Genesis:              time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
					SyncCommitteeIndices: []types.CommitteeIndex{types.CommitteeIndex(msg.Message.Contribution.SubcommitteeIndex * subCommitteeSize)},
				}
				msg.Message.Contribution.AggregationBits.SetBitAt(1, true)

				s.initCaches()
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationReject,
		},
		{
			name: "Invalid Proof Signature",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				s.cfg.beaconDB = database
				s.cfg.chain = chainService
				msg.Message.Contribution.BlockRoot = headRoot[:]
				hState, err := database.State(context.Background(), headRoot)
				assert.NoError(t, err)
				sc, err := hState.CurrentSyncCommittee()
				assert.NoError(t, err)
				var pubkey []byte
				for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
					coms, err := altair.SyncSubCommitteePubkeys(sc, types.CommitteeIndex(i))
					assert.NoError(t, err)
					for _, p := range coms {
						idx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(p))
						assert.Equal(t, true, ok)
						rt, err := syncSelectionProofSigningRoot(hState, slots.PrevSlot(hState.Slot()), types.CommitteeIndex(i))
						assert.NoError(t, err)
						sig := keys[idx].Sign(rt[:])
						isAggregator, err := altair.IsSyncCommitteeAggregator(sig.Marshal())
						require.NoError(t, err)
						if isAggregator {
							infiniteSig := [96]byte{0xC0}
							pubkey = keys[idx].PublicKey().Marshal()
							msg.Message.AggregatorIndex = idx
							msg.Message.SelectionProof = sig.Marshal()
							msg.Message.Contribution.Slot = slots.PrevSlot(hState.Slot())
							msg.Message.Contribution.SubcommitteeIndex = i
							msg.Message.Contribution.Signature = infiniteSig[:]
							msg.Message.Contribution.BlockRoot = headRoot[:]
							msg.Message.Contribution.AggregationBits = bitfield.NewBitvector128()
							msg.Message.Contribution.AggregationBits.SetBitAt(1, true)
							msg.Signature = infiniteSig[:]
							break
						}
					}
				}
				d, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainSyncCommitteeSelectionProof, hState.GenesisValidatorsRoot())
				require.NoError(t, err)
				subCommitteeSize := params.BeaconConfig().SyncCommitteeSize / params.BeaconConfig().SyncCommitteeSubnetCount
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot:           [32]byte{'A'},
					Genesis:                  time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
					SyncCommitteeIndices:     []types.CommitteeIndex{types.CommitteeIndex(msg.Message.Contribution.SubcommitteeIndex * subCommitteeSize)},
					PublicKey:                bytesutil.ToBytes48(pubkey),
					SyncSelectionProofDomain: d,
				}

				s.initCaches()
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationReject,
		},
		{
			name: "Invalid Sync Aggregate",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				s.cfg.beaconDB = database
				msg.Message.Contribution.BlockRoot = headRoot[:]
				hState, err := database.State(context.Background(), headRoot)
				assert.NoError(t, err)
				sc, err := hState.CurrentSyncCommittee()
				assert.NoError(t, err)
				for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
					coms, err := altair.SyncSubCommitteePubkeys(sc, types.CommitteeIndex(i))
					assert.NoError(t, err)
					for _, p := range coms {
						idx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(p))
						assert.Equal(t, true, ok)
						rt, err := syncSelectionProofSigningRoot(hState, slots.PrevSlot(hState.Slot()), types.CommitteeIndex(i))
						assert.NoError(t, err)
						sig := keys[idx].Sign(rt[:])
						isAggregator, err := altair.IsSyncCommitteeAggregator(sig.Marshal())
						require.NoError(t, err)
						if isAggregator {
							infiniteSig := [96]byte{0xC0}
							junkRoot := [32]byte{'A'}
							badSig := keys[idx].Sign(junkRoot[:])
							msg.Message.AggregatorIndex = idx
							msg.Message.SelectionProof = sig.Marshal()
							msg.Message.Contribution.Slot = slots.PrevSlot(hState.Slot())
							msg.Message.Contribution.SubcommitteeIndex = i
							msg.Message.Contribution.Signature = badSig.Marshal()
							msg.Message.Contribution.BlockRoot = headRoot[:]
							msg.Message.Contribution.AggregationBits = bitfield.NewBitvector128()
							msg.Message.Contribution.AggregationBits.SetBitAt(1, true)
							msg.Signature = infiniteSig[:]

							d, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainContributionAndProof, hState.GenesisValidatorsRoot())
							assert.NoError(t, err)
							sigRoot, err := signing.ComputeSigningRoot(msg.Message, d)
							assert.NoError(t, err)
							contrSig := keys[idx].Sign(sigRoot[:])

							msg.Signature = contrSig.Marshal()
							break
						}
					}
				}
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot:       [32]byte{'A'},
					Genesis:              time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
					SyncCommitteeIndices: []types.CommitteeIndex{1},
				}

				s.initCaches()
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationReject,
		},
		{
			name: "Invalid Signed Sync Contribution And Proof - Zero Bits Set",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				msg.Message.Contribution.BlockRoot = headRoot[:]
				s.cfg.beaconDB = database
				hState, err := database.State(context.Background(), headRoot)
				assert.NoError(t, err)
				sc, err := hState.CurrentSyncCommittee()
				assert.NoError(t, err)
				cd, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainContributionAndProof, hState.GenesisValidatorsRoot())
				assert.NoError(t, err)
				for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
					coms, err := altair.SyncSubCommitteePubkeys(sc, types.CommitteeIndex(i))
					assert.NoError(t, err)
					for _, p := range coms {
						idx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(p))
						assert.Equal(t, true, ok)
						rt, err := syncSelectionProofSigningRoot(hState, slots.PrevSlot(hState.Slot()), types.CommitteeIndex(i))
						assert.NoError(t, err)
						sig := keys[idx].Sign(rt[:])
						isAggregator, err := altair.IsSyncCommitteeAggregator(sig.Marshal())
						require.NoError(t, err)
						if isAggregator {
							infiniteSig := [96]byte{0xC0}
							msg.Message.AggregatorIndex = idx
							msg.Message.SelectionProof = sig.Marshal()
							msg.Message.Contribution.Slot = slots.PrevSlot(hState.Slot())
							msg.Message.Contribution.SubcommitteeIndex = i
							msg.Message.Contribution.Signature = infiniteSig[:]
							msg.Message.Contribution.BlockRoot = headRoot[:]
							msg.Message.Contribution.AggregationBits = bitfield.NewBitvector128()
							sigRoot, err := signing.ComputeSigningRoot(msg.Message, cd)
							assert.NoError(t, err)
							contrSig := keys[idx].Sign(sigRoot[:])

							msg.Signature = contrSig.Marshal()
							break
						}
					}
				}

				d, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainSyncCommitteeSelectionProof, hState.GenesisValidatorsRoot())
				require.NoError(t, err)
				subCommitteeSize := params.BeaconConfig().SyncCommitteeSize / params.BeaconConfig().SyncCommitteeSubnetCount
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot:              [32]byte{'A'},
					Genesis:                     time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
					SyncCommitteeIndices:        []types.CommitteeIndex{types.CommitteeIndex(msg.Message.Contribution.SubcommitteeIndex * subCommitteeSize)},
					PublicKey:                   bytesutil.ToBytes48(keys[msg.Message.AggregatorIndex].PublicKey().Marshal()),
					SyncSelectionProofDomain:    d,
					SyncContributionProofDomain: cd,
					SyncCommitteeDomain:         make([]byte, 32),
				}
				s.initCaches()
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationReject,
		},
		{
			name: "Valid Signed Sync Contribution And Proof - Single Bit Set",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				msg.Message.Contribution.BlockRoot = headRoot[:]
				s.cfg.beaconDB = database
				hState, err := database.State(context.Background(), headRoot)
				assert.NoError(t, err)
				sc, err := hState.CurrentSyncCommittee()
				assert.NoError(t, err)
				cd, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainContributionAndProof, hState.GenesisValidatorsRoot())
				assert.NoError(t, err)
				d, err := signing.Domain(hState.Fork(), slots.ToEpoch(hState.Slot()), params.BeaconConfig().DomainSyncCommittee, hState.GenesisValidatorsRoot())
				assert.NoError(t, err)
				var pubkeys [][]byte
				for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
					coms, err := altair.SyncSubCommitteePubkeys(sc, types.CommitteeIndex(i))
					pubkeys = coms
					assert.NoError(t, err)
					for _, p := range coms {
						idx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(p))
						assert.Equal(t, true, ok)
						rt, err := syncSelectionProofSigningRoot(hState, slots.PrevSlot(hState.Slot()), types.CommitteeIndex(i))
						assert.NoError(t, err)
						sig := keys[idx].Sign(rt[:])
						isAggregator, err := altair.IsSyncCommitteeAggregator(sig.Marshal())
						require.NoError(t, err)
						if isAggregator {
							msg.Message.AggregatorIndex = idx
							msg.Message.SelectionProof = sig.Marshal()
							msg.Message.Contribution.Slot = slots.PrevSlot(hState.Slot())
							msg.Message.Contribution.SubcommitteeIndex = i
							msg.Message.Contribution.BlockRoot = headRoot[:]
							msg.Message.Contribution.AggregationBits = bitfield.NewBitvector128()
							// Only Sign for 1 validator.
							rawBytes := p2ptypes.SSZBytes(headRoot[:])
							sigRoot, err := signing.ComputeSigningRoot(&rawBytes, d)
							assert.NoError(t, err)
							valIdx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(coms[0]))
							assert.Equal(t, true, ok)
							sig = keys[valIdx].Sign(sigRoot[:])
							msg.Message.Contribution.AggregationBits.SetBitAt(uint64(0), true)
							msg.Message.Contribution.Signature = sig.Marshal()

							sigRoot, err = signing.ComputeSigningRoot(msg.Message, cd)
							assert.NoError(t, err)
							contrSig := keys[idx].Sign(sigRoot[:])
							msg.Signature = contrSig.Marshal()
							break
						}
					}
				}

				pd, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainSyncCommitteeSelectionProof, hState.GenesisValidatorsRoot())
				require.NoError(t, err)
				subCommitteeSize := params.BeaconConfig().SyncCommitteeSize / params.BeaconConfig().SyncCommitteeSubnetCount
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot:              [32]byte{'A'},
					Genesis:                     time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
					SyncCommitteeIndices:        []types.CommitteeIndex{types.CommitteeIndex(msg.Message.Contribution.SubcommitteeIndex * subCommitteeSize)},
					PublicKey:                   bytesutil.ToBytes48(keys[msg.Message.AggregatorIndex].PublicKey().Marshal()),
					SyncSelectionProofDomain:    pd,
					SyncContributionProofDomain: cd,
					SyncCommitteeDomain:         d,
					SyncCommitteePubkeys:        pubkeys,
				}
				s.initCaches()
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationAccept,
		},
		{
			name: "Valid Signed Sync Contribution And Proof with Multiple Signatures",
			svc: NewService(context.Background(),
				WithP2P(mockp2p.NewTestP2P(t)),
				WithInitialSync(&mockSync.Sync{IsSyncing: false}),
				WithChainService(chainService),
				WithStateNotifier(chainService.StateNotifier()),
				WithOperationNotifier(chainService.OperationNotifier()),
			),
			setupSvc: func(s *Service, msg *ethpb.SignedContributionAndProof) *Service {
				s.cfg.stateGen = stategen.New(database)
				msg.Message.Contribution.BlockRoot = headRoot[:]
				s.cfg.beaconDB = database
				hState, err := database.State(context.Background(), headRoot)
				assert.NoError(t, err)
				sc, err := hState.CurrentSyncCommittee()
				assert.NoError(t, err)
				cd, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainContributionAndProof, hState.GenesisValidatorsRoot())
				assert.NoError(t, err)
				d, err := signing.Domain(hState.Fork(), slots.ToEpoch(hState.Slot()), params.BeaconConfig().DomainSyncCommittee, hState.GenesisValidatorsRoot())
				assert.NoError(t, err)
				var pubkeys [][]byte
				for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
					coms, err := altair.SyncSubCommitteePubkeys(sc, types.CommitteeIndex(i))
					pubkeys = coms
					assert.NoError(t, err)
					for _, p := range coms {
						idx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(p))
						assert.Equal(t, true, ok)
						rt, err := syncSelectionProofSigningRoot(hState, slots.PrevSlot(hState.Slot()), types.CommitteeIndex(i))
						assert.NoError(t, err)
						sig := keys[idx].Sign(rt[:])
						isAggregator, err := altair.IsSyncCommitteeAggregator(sig.Marshal())
						require.NoError(t, err)
						if isAggregator {
							msg.Message.AggregatorIndex = idx
							msg.Message.SelectionProof = sig.Marshal()
							msg.Message.Contribution.Slot = slots.PrevSlot(hState.Slot())
							msg.Message.Contribution.SubcommitteeIndex = i
							msg.Message.Contribution.BlockRoot = headRoot[:]
							msg.Message.Contribution.AggregationBits = bitfield.NewBitvector128()
							rawBytes := p2ptypes.SSZBytes(headRoot[:])
							sigRoot, err := signing.ComputeSigningRoot(&rawBytes, d)
							assert.NoError(t, err)
							var sigs []bls.Signature
							for i, p2 := range coms {
								idx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(p2))
								assert.Equal(t, true, ok)
								sig := keys[idx].Sign(sigRoot[:])
								sigs = append(sigs, sig)
								msg.Message.Contribution.AggregationBits.SetBitAt(uint64(i), true)
							}
							msg.Message.Contribution.Signature = bls.AggregateSignatures(sigs).Marshal()
							sigRoot, err = signing.ComputeSigningRoot(msg.Message, cd)
							assert.NoError(t, err)
							contrSig := keys[idx].Sign(sigRoot[:])
							msg.Signature = contrSig.Marshal()
							break
						}
					}
				}

				pd, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainSyncCommitteeSelectionProof, hState.GenesisValidatorsRoot())
				require.NoError(t, err)
				subCommitteeSize := params.BeaconConfig().SyncCommitteeSize / params.BeaconConfig().SyncCommitteeSubnetCount
				s.cfg.chain = &mockChain.ChainService{
					ValidatorsRoot:              [32]byte{'A'},
					Genesis:                     time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
					SyncCommitteeIndices:        []types.CommitteeIndex{types.CommitteeIndex(msg.Message.Contribution.SubcommitteeIndex * subCommitteeSize)},
					PublicKey:                   bytesutil.ToBytes48(keys[msg.Message.AggregatorIndex].PublicKey().Marshal()),
					SyncSelectionProofDomain:    pd,
					SyncContributionProofDomain: cd,
					SyncCommitteeDomain:         d,
					SyncCommitteePubkeys:        pubkeys,
				}

				s.initCaches()
				return s
			},
			args: args{
				ctx:   context.Background(),
				pid:   "random",
				topic: defaultTopic,
				msg: &ethpb.SignedContributionAndProof{
					Message: &ethpb.ContributionAndProof{
						AggregatorIndex: 1,
						Contribution: &ethpb.SyncCommitteeContribution{
							Slot:              1,
							SubcommitteeIndex: 1,
							BlockRoot:         params.BeaconConfig().ZeroHash[:],
							AggregationBits:   bitfield.NewBitvector128(),
							Signature:         emptySig[:],
						},
						SelectionProof: emptySig[:],
					},
					Signature: emptySig[:],
				}},
			want: pubsub.ValidationAccept,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.svc = tt.setupSvc(tt.svc, tt.args.msg)
			marshalledObj, err := tt.args.msg.MarshalSSZ()
			assert.NoError(t, err)
			marshalledObj = snappy.Encode(nil, marshalledObj)
			msg := &pubsub.Message{
				Message: &pubsubpb.Message{
					Data:  marshalledObj,
					Topic: &tt.args.topic,
				},
				ReceivedFrom:  "",
				ValidatorData: nil,
			}
			if got, err := tt.svc.validateSyncContributionAndProof(tt.args.ctx, tt.args.pid, msg); got != tt.want {
				_ = err
				t.Errorf("validateSyncContributionAndProof() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateSyncContributionAndProof(t *testing.T) {
	ctx := context.Background()
	database := testingdb.SetupDB(t)
	headRoot, keys := fillUpBlocksAndState(ctx, t, database)
	defaultTopic := p2p.SyncContributionAndProofSubnetTopicFormat
	defaultTopic = fmt.Sprintf(defaultTopic, []byte{0xAB, 0x00, 0xCC, 0x9E})
	defaultTopic = defaultTopic + "/" + encoder.ProtocolSuffixSSZSnappy
	emptySig := [96]byte{}
	pid := peer.ID("random")
	msg := &ethpb.SignedContributionAndProof{
		Message: &ethpb.ContributionAndProof{
			AggregatorIndex: 1,
			Contribution: &ethpb.SyncCommitteeContribution{
				Slot:              0,
				SubcommitteeIndex: 1,
				BlockRoot:         params.BeaconConfig().ZeroHash[:],
				AggregationBits:   bitfield.NewBitvector128(),
				Signature:         emptySig[:],
			},
			SelectionProof: emptySig[:],
		},
		Signature: emptySig[:],
	}
	chainService := &mockChain.ChainService{
		Genesis:        time.Now(),
		ValidatorsRoot: [32]byte{'A'},
	}
	s := NewService(context.Background(),
		WithP2P(mockp2p.NewTestP2P(t)),
		WithInitialSync(&mockSync.Sync{IsSyncing: false}),
		WithChainService(chainService),
		WithStateNotifier(chainService.StateNotifier()),
		WithOperationNotifier(chainService.OperationNotifier()),
	)
	go s.verifierRoutine()
	s.cfg.stateGen = stategen.New(database)
	msg.Message.Contribution.BlockRoot = headRoot[:]
	s.cfg.beaconDB = database
	hState, err := database.State(context.Background(), headRoot)
	assert.NoError(t, err)
	sc, err := hState.CurrentSyncCommittee()
	assert.NoError(t, err)
	cd, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainContributionAndProof, hState.GenesisValidatorsRoot())
	assert.NoError(t, err)
	d, err := signing.Domain(hState.Fork(), slots.ToEpoch(hState.Slot()), params.BeaconConfig().DomainSyncCommittee, hState.GenesisValidatorsRoot())
	assert.NoError(t, err)
	var pubkeys [][]byte
	for i := uint64(0); i < params.BeaconConfig().SyncCommitteeSubnetCount; i++ {
		coms, err := altair.SyncSubCommitteePubkeys(sc, types.CommitteeIndex(i))
		pubkeys = coms
		assert.NoError(t, err)
		for _, p := range coms {
			idx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(p))
			assert.Equal(t, true, ok)
			rt, err := syncSelectionProofSigningRoot(hState, slots.PrevSlot(hState.Slot()), types.CommitteeIndex(i))
			assert.NoError(t, err)
			sig := keys[idx].Sign(rt[:])
			isAggregator, err := altair.IsSyncCommitteeAggregator(sig.Marshal())
			require.NoError(t, err)
			if isAggregator {
				msg.Message.AggregatorIndex = idx
				msg.Message.SelectionProof = sig.Marshal()
				msg.Message.Contribution.Slot = slots.PrevSlot(hState.Slot())
				msg.Message.Contribution.SubcommitteeIndex = i
				msg.Message.Contribution.BlockRoot = headRoot[:]
				msg.Message.Contribution.AggregationBits = bitfield.NewBitvector128()
				// Only Sign for 1 validator.
				rawBytes := p2ptypes.SSZBytes(headRoot[:])
				sigRoot, err := signing.ComputeSigningRoot(&rawBytes, d)
				assert.NoError(t, err)
				valIdx, ok := hState.ValidatorIndexByPubkey(bytesutil.ToBytes48(coms[0]))
				assert.Equal(t, true, ok)
				sig = keys[valIdx].Sign(sigRoot[:])
				msg.Message.Contribution.AggregationBits.SetBitAt(uint64(0), true)
				msg.Message.Contribution.Signature = sig.Marshal()

				sigRoot, err = signing.ComputeSigningRoot(msg.Message, cd)
				assert.NoError(t, err)
				contrSig := keys[idx].Sign(sigRoot[:])
				msg.Signature = contrSig.Marshal()
				break
			}
		}
	}

	pd, err := signing.Domain(hState.Fork(), slots.ToEpoch(slots.PrevSlot(hState.Slot())), params.BeaconConfig().DomainSyncCommitteeSelectionProof, hState.GenesisValidatorsRoot())
	require.NoError(t, err)
	subCommitteeSize := params.BeaconConfig().SyncCommitteeSize / params.BeaconConfig().SyncCommitteeSubnetCount
	s.cfg.chain = &mockChain.ChainService{
		ValidatorsRoot:              [32]byte{'A'},
		Genesis:                     time.Now().Add(-time.Second * time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Duration(msg.Message.Contribution.Slot)),
		SyncCommitteeIndices:        []types.CommitteeIndex{types.CommitteeIndex(msg.Message.Contribution.SubcommitteeIndex * subCommitteeSize)},
		PublicKey:                   bytesutil.ToBytes48(keys[msg.Message.AggregatorIndex].PublicKey().Marshal()),
		SyncSelectionProofDomain:    pd,
		SyncContributionProofDomain: cd,
		SyncCommitteeDomain:         d,
		SyncCommitteePubkeys:        pubkeys,
	}
	s.initCaches()

	marshalledObj, err := msg.MarshalSSZ()
	assert.NoError(t, err)
	marshalledObj = snappy.Encode(nil, marshalledObj)
	pubsubMsg := &pubsub.Message{
		Message: &pubsubpb.Message{
			Data:  marshalledObj,
			Topic: &defaultTopic,
		},
		ReceivedFrom:  "",
		ValidatorData: nil,
	}

	// Subscribe to operation notifications.
	opChannel := make(chan *feed.Event, 1)
	opSub := s.cfg.operationNotifier.OperationFeed().Subscribe(opChannel)
	defer opSub.Unsubscribe()

	_, err = s.validateSyncContributionAndProof(ctx, pid, pubsubMsg)
	require.NoError(t, err)

	// Ensure the state notification was broadcast.
	notificationFound := false
	for !notificationFound {
		select {
		case event := <-opChannel:
			if event.Type == opfeed.SyncCommitteeContributionReceived {
				notificationFound = true
				_, ok := event.Data.(*opfeed.SyncCommitteeContributionReceivedData)
				assert.Equal(t, true, ok, "Entity is not of type *opfeed.SyncCommitteeContributionReceivedData")
			}
		case <-opSub.Err():
			t.Error("Subscription to state notifier failed")
			return
		}
	}
}

func TestValidateSyncContributionAndProof_Optimistic(t *testing.T) {
	p := mockp2p.NewTestP2P(t)
	ctx := context.Background()

	slashing, s := setupValidAttesterSlashing(t)

	r := &Service{
		cfg: &config{
			p2p:         p,
			chain:       &mockChain.ChainService{State: s, Optimistic: true},
			initialSync: &mockSync.Sync{IsSyncing: false},
		},
	}

	buf := new(bytes.Buffer)
	_, err := p.Encoding().EncodeGossip(buf, slashing)
	require.NoError(t, err)

	topic := p2p.GossipTypeMapping[reflect.TypeOf(slashing)]
	msg := &pubsub.Message{
		Message: &pubsubpb.Message{
			Data:  buf.Bytes(),
			Topic: &topic,
		},
	}
	res, err := r.validateCommitteeIndexBeaconAttestation(ctx, "foobar", msg)
	assert.NoError(t, err)
	valid := res == pubsub.ValidationIgnore
	assert.Equal(t, true, valid, "Should have ignore this message")
}

func fillUpBlocksAndState(ctx context.Context, t *testing.T, beaconDB db.Database) ([32]byte, []bls.SecretKey) {
	gs, keys := util.DeterministicGenesisStateAltair(t, 64)
	sCom, err := altair.NextSyncCommittee(ctx, gs)
	assert.NoError(t, err)
	assert.NoError(t, gs.SetCurrentSyncCommittee(sCom))
	assert.NoError(t, beaconDB.SaveGenesisData(context.Background(), gs))

	testState := gs.Copy()
	hRoot := [32]byte{}
	for i := types.Slot(1); i <= params.BeaconConfig().SlotsPerEpoch; i++ {
		blk, err := util.GenerateFullBlockAltair(testState, keys, util.DefaultBlockGenConfig(), i)
		require.NoError(t, err)
		r, err := blk.Block.HashTreeRoot()
		require.NoError(t, err)
		wsb, err := blocks.NewSignedBeaconBlock(blk)
		require.NoError(t, err)
		_, testState, err = transition.ExecuteStateTransitionNoVerifyAnySig(ctx, testState, wsb)
		assert.NoError(t, err)
		assert.NoError(t, beaconDB.SaveBlock(ctx, wsb))
		assert.NoError(t, beaconDB.SaveStateSummary(ctx, &ethpb.StateSummary{Slot: i, Root: r[:]}))
		assert.NoError(t, beaconDB.SaveState(ctx, testState, r))
		require.NoError(t, beaconDB.SaveHeadBlockRoot(ctx, r))
		hRoot = r
	}
	return hRoot, keys
}

func syncSelectionProofSigningRoot(st state.BeaconState, slot types.Slot, comIdx types.CommitteeIndex) ([32]byte, error) {
	dom, err := signing.Domain(st.Fork(), slots.ToEpoch(slot), params.BeaconConfig().DomainSyncCommitteeSelectionProof, st.GenesisValidatorsRoot())
	if err != nil {
		return [32]byte{}, err
	}
	selectionData := &ethpb.SyncAggregatorSelectionData{Slot: slot, SubcommitteeIndex: uint64(comIdx)}
	return signing.ComputeSigningRoot(selectionData, dom)
}

func TestService_setSyncContributionIndexSlotSeen(t *testing.T) {
	chainService := &mockChain.ChainService{
		Genesis:        time.Now(),
		ValidatorsRoot: [32]byte{'A'},
	}
	s := NewService(context.Background(), WithP2P(mockp2p.NewTestP2P(t)), WithStateNotifier(chainService.StateNotifier()))
	s.initCaches()

	// Empty cache
	b0 := bitfield.NewBitvector128()
	b0.SetBitAt(0, true)
	has, err := s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		AggregationBits: b0,
	})
	require.NoError(t, err)
	require.Equal(t, false, has)

	// Cache with entries but same key
	require.NoError(t, s.setSyncContributionBits(&ethpb.SyncCommitteeContribution{
		AggregationBits: b0,
	}))
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		AggregationBits: b0,
	})
	require.NoError(t, err)
	require.Equal(t, true, has)
	b1 := bitfield.NewBitvector128()
	b1.SetBitAt(1, true)
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		AggregationBits: b1,
	})
	require.NoError(t, err)
	require.Equal(t, false, has)
	b2 := bitfield.NewBitvector128()
	b2.SetBitAt(1, true)
	b2.SetBitAt(2, true)
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		AggregationBits: b2,
	})
	require.NoError(t, err)
	require.Equal(t, false, has)
	b2.SetBitAt(0, true)
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		AggregationBits: b2,
	})
	require.NoError(t, err)
	require.Equal(t, true, has)

	// Make sure set doesn't contain existing overlaps
	require.Equal(t, 1, s.syncContributionBitsOverlapCache.Len())

	// Cache with entries but different key
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		Slot:              1,
		SubcommitteeIndex: 2,
		BlockRoot:         []byte{'A'},
		AggregationBits:   b0,
	})
	require.NoError(t, err)
	require.Equal(t, false, has)
	require.NoError(t, s.setSyncContributionBits(&ethpb.SyncCommitteeContribution{
		Slot:              1,
		SubcommitteeIndex: 2,
		BlockRoot:         []byte{'A'},
		AggregationBits:   b2,
	}))
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		Slot:              1,
		SubcommitteeIndex: 2,
		BlockRoot:         []byte{'A'},
		AggregationBits:   b0,
	})
	require.NoError(t, err)
	require.Equal(t, true, has)
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		Slot:              1,
		SubcommitteeIndex: 2,
		BlockRoot:         []byte{'A'},
		AggregationBits:   b1,
	})
	require.NoError(t, err)
	require.Equal(t, true, has)
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		Slot:              1,
		SubcommitteeIndex: 2,
		BlockRoot:         []byte{'A'},
		AggregationBits:   b2,
	})
	require.NoError(t, err)
	require.Equal(t, true, has)

	// Check invariant with the keys
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		Slot:              2,
		SubcommitteeIndex: 2,
		BlockRoot:         []byte{'A'},
		AggregationBits:   b0,
	})
	require.NoError(t, err)
	require.Equal(t, false, has)
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		Slot:              1,
		SubcommitteeIndex: 2,
		BlockRoot:         []byte{'B'},
		AggregationBits:   b0,
	})
	require.NoError(t, err)
	require.Equal(t, false, has)
	has, err = s.hasSeenSyncContributionBits(&ethpb.SyncCommitteeContribution{
		Slot:              1,
		SubcommitteeIndex: 3,
		BlockRoot:         []byte{'A'},
		AggregationBits:   b0,
	})
	require.NoError(t, err)
	require.Equal(t, false, has)
}
