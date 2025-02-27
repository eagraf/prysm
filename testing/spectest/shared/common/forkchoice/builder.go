package forkchoice

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/blockchain"
	"github.com/prysmaticlabs/prysm/v3/beacon-chain/state"
	"github.com/prysmaticlabs/prysm/v3/config/params"
	"github.com/prysmaticlabs/prysm/v3/consensus-types/interfaces"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/encoding/bytesutil"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/v3/testing/require"
)

type Builder struct {
	service  *blockchain.Service
	lastTick int64
	execMock *engineMock
}

func NewBuilder(t testing.TB, initialState state.BeaconState, initialBlock interfaces.SignedBeaconBlock) *Builder {
	execMock := &engineMock{
		powBlocks: make(map[[32]byte]*ethpb.PowBlock),
	}
	service := startChainService(t, initialState, initialBlock, execMock)
	return &Builder{
		service:  service,
		execMock: execMock,
	}
}

// Tick resets the genesis time to now()-tick and adjusts the slot to the appropriate value.
func (bb *Builder) Tick(t testing.TB, tick int64) {
	bb.service.SetGenesisTime(time.Unix(time.Now().Unix()-tick, 0))
	bb.service.ForkChoicer().SetGenesisTime(uint64(time.Now().Unix() - tick))
	if tick > bb.lastTick {
		slot := uint64(tick) / params.BeaconConfig().SecondsPerSlot
		require.NoError(t, bb.service.ForkChoicer().NewSlot(context.TODO(), types.Slot(slot)))
		bb.lastTick = tick
	}
}

// block returns the block root.
func (bb *Builder) block(t testing.TB, b interfaces.SignedBeaconBlock) [32]byte {
	r, err := b.Block().HashTreeRoot()
	require.NoError(t, err)
	return r
}

// InvalidBlock receives the invalid block and notifies forkchoice.
func (bb *Builder) InvalidBlock(t testing.TB, b interfaces.SignedBeaconBlock) {
	r := bb.block(t, b)
	require.Equal(t, true, bb.service.ReceiveBlock(context.TODO(), b, r) != nil)
}

// ValidBlock receives the valid block and notifies forkchoice.
func (bb *Builder) ValidBlock(t testing.TB, b interfaces.SignedBeaconBlock) {
	r := bb.block(t, b)
	require.NoError(t, bb.service.ReceiveBlock(context.TODO(), b, r))
}

// PoWBlock receives the block and notifies a mocked execution engine.
func (bb *Builder) PoWBlock(pb *ethpb.PowBlock) {
	bb.execMock.powBlocks[bytesutil.ToBytes32(pb.BlockHash)] = pb
}

// Attestation receives the attestation and updates forkchoice.
func (bb *Builder) Attestation(t testing.TB, a *ethpb.Attestation) {
	require.NoError(t, bb.service.OnAttestation(context.TODO(), a))
}

// AttesterSlashing receives an attester slashing and feeds it to forkchoice.
func (bb *Builder) AttesterSlashing(s *ethpb.AttesterSlashing) {
	slashings := []*ethpb.AttesterSlashing{s}
	bb.service.InsertSlashingsToForkChoiceStore(context.TODO(), slashings)
}

// Check evaluates the fork choice results and compares them to the expected values.
func (bb *Builder) Check(t testing.TB, c *Check) {
	if c == nil {
		return
	}
	ctx := context.TODO()
	require.NoError(t, bb.service.UpdateAndSaveHeadWithBalances(ctx))
	if c.Head != nil {
		r, err := bb.service.HeadRoot(ctx)
		require.NoError(t, err)
		require.DeepEqual(t, common.FromHex(c.Head.Root), r)
		require.Equal(t, types.Slot(c.Head.Slot), bb.service.HeadSlot())
	}
	if c.JustifiedCheckPoint != nil {
		cp := &ethpb.Checkpoint{
			Epoch: types.Epoch(c.JustifiedCheckPoint.Epoch),
			Root:  common.FromHex(c.JustifiedCheckPoint.Root),
		}
		got := bb.service.CurrentJustifiedCheckpt()
		require.DeepEqual(t, cp, got)
	}
	if c.BestJustifiedCheckPoint != nil {
		cp := &ethpb.Checkpoint{
			Epoch: types.Epoch(c.BestJustifiedCheckPoint.Epoch),
			Root:  common.FromHex(c.BestJustifiedCheckPoint.Root),
		}
		got := bb.service.BestJustifiedCheckpt()
		require.DeepEqual(t, cp, got)
	}
	if c.FinalizedCheckPoint != nil {
		cp := &ethpb.Checkpoint{
			Epoch: types.Epoch(c.FinalizedCheckPoint.Epoch),
			Root:  common.FromHex(c.FinalizedCheckPoint.Root),
		}
		got := bb.service.FinalizedCheckpt()
		require.DeepSSZEqual(t, cp, got)
	}
	if c.ProposerBoostRoot != nil {
		want := fmt.Sprintf("%#x", common.FromHex(*c.ProposerBoostRoot))
		got := fmt.Sprintf("%#x", bb.service.ForkChoiceStore().ProposerBoost())
		require.DeepEqual(t, want, got)
	}

}
