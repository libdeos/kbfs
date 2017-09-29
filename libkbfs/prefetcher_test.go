// Copyright 2016 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

package libkbfs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/keybase/go-codec/codec"
	"github.com/stretchr/testify/require"
)

func makeRandomBlockInfo(t *testing.T) BlockInfo {
	return BlockInfo{
		makeRandomBlockPointer(t),
		150,
	}
}

func makeRandomDirEntry(
	t *testing.T, typ EntryType, size uint64, path string) DirEntry {
	return DirEntry{
		makeRandomBlockInfo(t),
		EntryInfo{
			typ,
			size,
			path,
			101,
			102,
			"",
		},
		codec.UnknownFieldSetHandler{},
	}
}
func makeFakeIndirectFilePtr(t *testing.T, off int64) IndirectFilePtr {
	return IndirectFilePtr{
		makeRandomBlockInfo(t),
		off,
		false,
		codec.UnknownFieldSetHandler{},
	}
}

func makeFakeIndirectDirPtr(t *testing.T, off string) IndirectDirPtr {
	return IndirectDirPtr{
		makeRandomBlockInfo(t),
		off,
		codec.UnknownFieldSetHandler{},
	}
}

func makeFakeDirBlock(t *testing.T, name string) *DirBlock {
	return &DirBlock{Children: map[string]DirEntry{
		name: makeRandomDirEntry(t, Dir, 100, name),
	}}
}

func initPrefetcherTest(t *testing.T) (*blockRetrievalQueue,
	*fakeBlockGetter, *testBlockRetrievalConfig) {
	// We don't want the block getter to respect cancelation, because we need
	// <-q.Prefetcher().Shutdown() to represent whether the retrieval requests
	// _actually_ completed.
	bg := newFakeBlockGetter(false)
	config := newTestBlockRetrievalConfig(t, bg, nil)
	q := newBlockRetrievalQueue(1, 1, config)
	require.NotNil(t, q)

	return q, bg, config
}

func shutdownPrefetcherTest(q *blockRetrievalQueue) {
	q.Shutdown()
}

func testPrefetcherCheckGet(t *testing.T, bcache BlockCache, ptr BlockPointer,
	expectedBlock Block, expectedPrefetchStatus PrefetchStatus,
	expectedLifetime BlockCacheLifetime) {
	block, prefetchStatus, lifetime, err := bcache.GetWithPrefetch(ptr)
	require.NoError(t, err)
	require.Equal(t, expectedBlock, block)
	require.Equal(t, expectedPrefetchStatus.String(), prefetchStatus.String())
	require.Equal(t, expectedLifetime.String(), lifetime.String())
}

func waitForPrefetchOrBust(t *testing.T, ch <-chan struct{}) {
	// TODO: add t.Helper() once we're on Go 1.9
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("Failed to wait for prefetch.")
	}
}

func TestPrefetcherIndirectFileBlock(t *testing.T) {
	t.Log("Test indirect file block prefetching.")
	q, bg, config := initPrefetcherTest(t)
	defer shutdownPrefetcherTest(q)

	t.Log("Initialize an indirect file block pointing to 2 file data blocks.")
	ptrs := []IndirectFilePtr{
		makeFakeIndirectFilePtr(t, 0),
		makeFakeIndirectFilePtr(t, 150),
	}
	rootPtr := makeRandomBlockPointer(t)
	rootBlock := &FileBlock{IPtrs: ptrs}
	rootBlock.IsInd = true
	indBlock1 := makeFakeFileBlock(t, true)
	indBlock2 := makeFakeFileBlock(t, true)

	_, continueChRootBlock := bg.setBlockToReturn(rootPtr, rootBlock)
	_, continueChIndBlock1 :=
		bg.setBlockToReturn(ptrs[0].BlockPointer, indBlock1)
	_, continueChIndBlock2 :=
		bg.setBlockToReturn(ptrs[1].BlockPointer, indBlock2)

	var block Block = &FileBlock{}
	ch := q.Request(context.Background(),
		defaultOnDemandRequestPriority, makeKMD(), rootPtr, block,
		TransientEntry)
	continueChRootBlock <- nil
	err := <-ch
	require.NoError(t, err)
	require.Equal(t, rootBlock, block)

	t.Log("Release the prefetched indirect blocks.")
	continueChIndBlock1 <- nil
	continueChIndBlock2 <- nil

	t.Log("Wait for the prefetch to finish.")
	<-q.Prefetcher().Shutdown()

	t.Log("Ensure that the prefetched blocks are in the cache.")
	testPrefetcherCheckGet(t, config.BlockCache(), rootPtr, rootBlock,
		TriggeredPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(), ptrs[0].BlockPointer,
		indBlock1, NoPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(), ptrs[1].BlockPointer,
		indBlock2, NoPrefetch, TransientEntry)
}

func TestPrefetcherIndirectDirBlock(t *testing.T) {
	t.Log("Test indirect dir block prefetching.")
	q, bg, config := initPrefetcherTest(t)
	defer shutdownPrefetcherTest(q)

	t.Log("Initialize an indirect dir block pointing to 2 dir data blocks.")
	ptrs := []IndirectDirPtr{
		makeFakeIndirectDirPtr(t, "a"),
		makeFakeIndirectDirPtr(t, "b"),
	}
	rootPtr := makeRandomBlockPointer(t)
	rootBlock := &DirBlock{IPtrs: ptrs, Children: make(map[string]DirEntry)}
	rootBlock.IsInd = true
	indBlock1 := makeFakeDirBlock(t, "a")
	indBlock2 := makeFakeDirBlock(t, "b")

	_, continueChRootBlock := bg.setBlockToReturn(rootPtr, rootBlock)
	_, continueChIndBlock1 :=
		bg.setBlockToReturn(ptrs[0].BlockPointer, indBlock1)
	_, continueChIndBlock2 :=
		bg.setBlockToReturn(ptrs[1].BlockPointer, indBlock2)

	block := NewDirBlock()
	ch := q.Request(context.Background(),
		defaultOnDemandRequestPriority, makeKMD(), rootPtr, block,
		TransientEntry)
	continueChRootBlock <- nil
	err := <-ch
	require.NoError(t, err)
	require.Equal(t, rootBlock, block)

	t.Log("Release the prefetched indirect blocks.")
	continueChIndBlock1 <- nil
	continueChIndBlock2 <- nil

	t.Log("Wait for the prefetch to finish.")
	<-q.Prefetcher().Shutdown()

	t.Log("Ensure that the prefetched blocks are in the cache.")
	testPrefetcherCheckGet(t, config.BlockCache(), rootPtr, rootBlock,
		TriggeredPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(), ptrs[0].BlockPointer,
		indBlock1, NoPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(), ptrs[1].BlockPointer,
		indBlock2, NoPrefetch, TransientEntry)
}

func TestPrefetcherDirectDirBlock(t *testing.T) {
	t.Log("Test direct dir block prefetching.")
	q, bg, config := initPrefetcherTest(t)
	defer shutdownPrefetcherTest(q)

	t.Log("Initialize a direct dir block with entries pointing to 3 files.")
	fileA := makeFakeFileBlock(t, true)
	fileC := makeFakeFileBlock(t, true)
	rootPtr := makeRandomBlockPointer(t)
	rootDir := &DirBlock{Children: map[string]DirEntry{
		"a": makeRandomDirEntry(t, File, 100, "a"),
		"b": makeRandomDirEntry(t, Dir, 60, "b"),
		"c": makeRandomDirEntry(t, Exec, 20, "c"),
	}}
	dirB := &DirBlock{Children: map[string]DirEntry{
		"d": makeRandomDirEntry(t, File, 100, "d"),
	}}
	dirBfileD := makeFakeFileBlock(t, true)

	_, continueChRootDir := bg.setBlockToReturn(rootPtr, rootDir)
	_, continueChFileA :=
		bg.setBlockToReturn(rootDir.Children["a"].BlockPointer, fileA)
	_, continueChDirB :=
		bg.setBlockToReturn(rootDir.Children["b"].BlockPointer, dirB)
	_, continueChFileC :=
		bg.setBlockToReturn(rootDir.Children["c"].BlockPointer, fileC)
	_, _ = bg.setBlockToReturn(dirB.Children["d"].BlockPointer, dirBfileD)

	var block Block = &DirBlock{}
	ch := q.Request(context.Background(),
		defaultOnDemandRequestPriority, makeKMD(), rootPtr, block,
		TransientEntry)
	continueChRootDir <- nil
	err := <-ch
	require.NoError(t, err)
	require.Equal(t, rootDir, block)

	t.Log("Release the blocks in ascending order of their size. The largest " +
		"block will error.")
	continueChFileC <- nil
	continueChDirB <- nil
	continueChFileA <- context.Canceled
	t.Log("Wait for the prefetch to finish.")
	<-q.Prefetcher().Shutdown()

	t.Log("Ensure that the prefetched blocks are in the cache.")
	testPrefetcherCheckGet(t, config.BlockCache(), rootPtr, rootDir,
		TriggeredPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		rootDir.Children["c"].BlockPointer, fileC, NoPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		rootDir.Children["b"].BlockPointer, dirB, NoPrefetch, TransientEntry)

	t.Log("Ensure that the largest block isn't in the cache.")
	block, err = config.BlockCache().Get(rootDir.Children["a"].BlockPointer)
	require.EqualError(t, err,
		NoSuchBlockError{rootDir.Children["a"].BlockPointer.ID}.Error())
	t.Log("Ensure that the second-level directory didn't cause a prefetch.")
	block, err = config.BlockCache().Get(dirB.Children["d"].BlockPointer)
	require.EqualError(t, err,
		NoSuchBlockError{dirB.Children["d"].BlockPointer.ID}.Error())
}

func TestPrefetcherAlreadyCached(t *testing.T) {
	t.Log("Test direct dir block prefetching when the dir block is cached.")
	q, bg, config := initPrefetcherTest(t)
	cache := config.BlockCache()
	defer shutdownPrefetcherTest(q)

	t.Log("Initialize a direct dir block with an entry pointing to 1 " +
		"folder, which in turn points to 1 file.")
	fileB := makeFakeFileBlock(t, true)
	rootPtr := makeRandomBlockPointer(t)
	rootDir := &DirBlock{Children: map[string]DirEntry{
		"a": makeRandomDirEntry(t, Dir, 60, "a"),
	}}
	dirA := &DirBlock{Children: map[string]DirEntry{
		"b": makeRandomDirEntry(t, File, 100, "b"),
	}}

	_, continueChRootDir := bg.setBlockToReturn(rootPtr, rootDir)
	_, continueChDirA :=
		bg.setBlockToReturn(rootDir.Children["a"].BlockPointer, dirA)
	_, continueChFileB :=
		bg.setBlockToReturn(dirA.Children["b"].BlockPointer, fileB)

	t.Log("Request the root block.")
	kmd := makeKMD()
	var block Block = &DirBlock{}
	ch := q.Request(context.Background(),
		defaultOnDemandRequestPriority, kmd, rootPtr, block, TransientEntry)
	continueChRootDir <- nil
	err := <-ch
	require.NoError(t, err)
	require.Equal(t, rootDir, block)

	t.Log("Release the prefetch for dirA.")
	continueChDirA <- nil
	t.Log("Wait for the prefetch to finish.")
	<-q.Prefetcher().Shutdown()
	q.TogglePrefetcher(context.Background(), true, nil)

	t.Log("Ensure that the prefetched block is in the cache.")
	block, err = cache.Get(rootDir.Children["a"].BlockPointer)
	require.NoError(t, err)
	require.Equal(t, dirA, block)
	t.Log("Ensure that the second-level directory didn't cause a prefetch.")
	block, err = cache.Get(dirA.Children["b"].BlockPointer)
	require.EqualError(t, err,
		NoSuchBlockError{dirA.Children["b"].BlockPointer.ID}.Error())

	t.Log("Request the already-cached second-level directory block. We don't " +
		"need to unblock this one.")
	block = &DirBlock{}
	ch = q.Request(context.Background(),
		defaultOnDemandRequestPriority, kmd,
		rootDir.Children["a"].BlockPointer, block, TransientEntry)
	err = <-ch
	require.NoError(t, err)
	require.Equal(t, dirA, block)

	t.Log("Release the prefetch for fileB.")
	continueChFileB <- nil
	t.Log("Wait for the prefetch to finish.")
	<-q.Prefetcher().Shutdown()
	q.TogglePrefetcher(context.Background(), true, nil)

	testPrefetcherCheckGet(t, cache, dirA.Children["b"].BlockPointer, fileB,
		NoPrefetch, TransientEntry)
	// Check that the dir block is marked as having been prefetched.
	testPrefetcherCheckGet(t, cache, rootDir.Children["a"].BlockPointer, dirA,
		TriggeredPrefetch, TransientEntry)

	t.Log("Remove the prefetched file block from the cache.")
	cache.DeleteTransient(dirA.Children["b"].BlockPointer, kmd.TlfID())
	_, err = cache.Get(dirA.Children["b"].BlockPointer)
	require.EqualError(t, err,
		NoSuchBlockError{dirA.Children["b"].BlockPointer.ID}.Error())

	t.Log("Request the second-level directory block again. No prefetches " +
		"should be triggered.")
	block = &DirBlock{}
	ch = q.Request(context.Background(),
		defaultOnDemandRequestPriority, kmd,
		rootDir.Children["a"].BlockPointer, block, TransientEntry)
	err = <-ch
	require.NoError(t, err)
	require.Equal(t, dirA, block)

	t.Log("Wait for the prefetch to finish.")
	<-q.Prefetcher().Shutdown()
}

func TestPrefetcherNoRepeatedPrefetch(t *testing.T) {
	t.Log("Test that prefetches are only triggered once for a given block.")
	q, bg, config := initPrefetcherTest(t)
	cache := config.BlockCache().(*BlockCacheStandard)
	defer shutdownPrefetcherTest(q)

	t.Log("Initialize a direct dir block with an entry pointing to 1 file.")
	fileA := makeFakeFileBlock(t, true)
	rootPtr := makeRandomBlockPointer(t)
	rootDir := &DirBlock{Children: map[string]DirEntry{
		"a": makeRandomDirEntry(t, File, 60, "a"),
	}}
	ptrA := rootDir.Children["a"].BlockPointer

	_, continueChRootDir := bg.setBlockToReturn(rootPtr, rootDir)
	_, continueChFileA := bg.setBlockToReturn(ptrA, fileA)

	t.Log("Request the root block.")
	var block Block = &DirBlock{}
	kmd := makeKMD()
	ch := q.Request(context.Background(),
		defaultOnDemandRequestPriority, kmd, rootPtr, block, TransientEntry)
	continueChRootDir <- nil
	err := <-ch
	require.NoError(t, err)
	require.Equal(t, rootDir, block)

	t.Log("Release the prefetched block.")
	continueChFileA <- nil

	t.Log("Wait for the prefetch to finish, then verify that the prefetched " +
		"block is in the cache.")
	<-q.Prefetcher().Shutdown()
	q.TogglePrefetcher(context.Background(), true, nil)
	testPrefetcherCheckGet(t, config.BlockCache(), ptrA, fileA, NoPrefetch,
		TransientEntry)

	t.Log("Remove the prefetched block from the cache.")
	cache.DeleteTransient(ptrA, kmd.TlfID())
	_, err = cache.Get(ptrA)
	require.EqualError(t, err, NoSuchBlockError{ptrA.ID}.Error())

	t.Log("Request the root block again. It should be cached, so it should " +
		"return without needing to release the block.")
	block = &DirBlock{}
	ch = q.Request(context.Background(),
		defaultOnDemandRequestPriority, kmd, rootPtr, block, TransientEntry)
	err = <-ch
	require.NoError(t, err)
	require.Equal(t, rootDir, block)

	t.Log("Wait for the prefetch to finish, then verify that the child " +
		"block is still not in the cache.")
	_, err = cache.Get(ptrA)
	require.EqualError(t, err, NoSuchBlockError{ptrA.ID}.Error())
	<-q.Prefetcher().Shutdown()
}

func TestPrefetcherEmptyDirectDirBlock(t *testing.T) {
	t.Log("Test empty direct dir block prefetching.")
	q, bg, config := initPrefetcherTest(t)
	defer shutdownPrefetcherTest(q)

	t.Log("Initialize an empty direct dir block.")
	rootPtr := makeRandomBlockPointer(t)
	rootDir := &DirBlock{Children: map[string]DirEntry{}}

	_, continueChRootDir := bg.setBlockToReturn(rootPtr, rootDir)

	var block Block = &DirBlock{}
	ch := q.Request(context.Background(),
		defaultOnDemandRequestPriority, makeKMD(), rootPtr, block,
		TransientEntry)
	continueChRootDir <- nil
	err := <-ch
	require.NoError(t, err)
	require.Equal(t, rootDir, block)

	t.Log("Wait for prefetching to complete.")
	<-q.Prefetcher().Shutdown()

	t.Log("Ensure that the directory block is in the cache.")
	testPrefetcherCheckGet(t, config.BlockCache(), rootPtr, rootDir,
		FinishedPrefetch, TransientEntry)
}

func notifyContinueCh(ch chan<- error, wg *sync.WaitGroup) {
	go func() {
		ch <- nil
		wg.Done()
	}()
}

func TestPrefetcherForSyncedTLF(t *testing.T) {
	t.Log("Test synced TLF prefetching.")
	q, bg, config := initPrefetcherTest(t)
	defer shutdownPrefetcherTest(q)
	prefetchSyncCh := make(chan struct{})
	q.TogglePrefetcher(context.Background(), true, prefetchSyncCh)
	prefetchSyncCh <- struct{}{}

	kmd := makeKMD()
	config.SetTlfSyncState(kmd.TlfID(), true)

	t.Log("Initialize a direct dir block with entries pointing to 2 files " +
		"and 1 directory. The directory has an entry pointing to another " +
		"file, which has 2 indirect blocks.")
	fileA := makeFakeFileBlock(t, true)
	fileC := makeFakeFileBlock(t, true)
	rootPtr := makeRandomBlockPointer(t)
	rootDir := &DirBlock{Children: map[string]DirEntry{
		"a": makeRandomDirEntry(t, File, 100, "a"),
		"b": makeRandomDirEntry(t, Dir, 60, "b"),
		"c": makeRandomDirEntry(t, Exec, 20, "c"),
	}}
	dirB := &DirBlock{Children: map[string]DirEntry{
		"d": makeRandomDirEntry(t, File, 100, "d"),
	}}
	dirBfileDptrs := []IndirectFilePtr{
		makeFakeIndirectFilePtr(t, 0),
		makeFakeIndirectFilePtr(t, 150),
	}
	dirBfileD := &FileBlock{IPtrs: dirBfileDptrs}
	dirBfileD.IsInd = true
	dirBfileDblock1 := makeFakeFileBlock(t, true)
	dirBfileDblock2 := makeFakeFileBlock(t, true)

	_, continueChRootDir := bg.setBlockToReturn(rootPtr, rootDir)
	_, continueChFileA :=
		bg.setBlockToReturn(rootDir.Children["a"].BlockPointer, fileA)
	_, continueChDirB :=
		bg.setBlockToReturn(rootDir.Children["b"].BlockPointer, dirB)
	_, continueChFileC :=
		bg.setBlockToReturn(rootDir.Children["c"].BlockPointer, fileC)
	_, continueChDirBfileD :=
		bg.setBlockToReturn(dirB.Children["d"].BlockPointer, dirBfileD)

	_, continueChDirBfileDblock1 :=
		bg.setBlockToReturn(dirBfileDptrs[0].BlockPointer, dirBfileDblock1)
	_, continueChDirBfileDblock2 :=
		bg.setBlockToReturn(dirBfileDptrs[1].BlockPointer, dirBfileDblock2)

	var block Block = &DirBlock{}
	ch := q.Request(context.Background(),
		defaultOnDemandRequestPriority, kmd, rootPtr, block, TransientEntry)
	continueChRootDir <- nil
	err := <-ch
	require.NoError(t, err)
	require.Equal(t, rootDir, block)

	t.Log("Release all the blocks.")
	wg := &sync.WaitGroup{}
	wg.Add(4)
	go func() {
		continueChFileC <- nil
		continueChDirB <- nil
		// After this, the prefetch worker can either pick up the third child of
		// dir1 (continueCh2), or the first child of dir2 (continueCh5).
		// TODO: The prefetcher should have a "global" prefetch priority
		// reservation system that goes down with each next set of prefetches.
		notifyContinueCh(continueChFileA, wg)
		notifyContinueCh(continueChDirBfileD, wg)
		notifyContinueCh(continueChDirBfileDblock1, wg)
		notifyContinueCh(continueChDirBfileDblock2, wg)
	}()
	t.Log("Wait for prefetching to complete.")
	// First we wait for all prefetches to be triggered.
	// Release after prefetching rootDir
	prefetchSyncCh <- struct{}{}
	// Release after prefetching fileC
	prefetchSyncCh <- struct{}{}
	// Release after prefetching dirB
	prefetchSyncCh <- struct{}{}
	// Release after prefetching fileA
	prefetchSyncCh <- struct{}{}
	// Release after prefetching dirBfileD
	prefetchSyncCh <- struct{}{}
	// Release after prefetching dirBfileDblock1
	prefetchSyncCh <- struct{}{}
	// Release after prefetching dirBfileDblock2
	prefetchSyncCh <- struct{}{}
	wg.Wait()
	// Then we wait for the pending prefetches to complete.
	<-q.Prefetcher().Shutdown()
	q.TogglePrefetcher(context.Background(), true, prefetchSyncCh)
	prefetchSyncCh <- struct{}{}

	t.Log("Ensure that the prefetched blocks are all in the cache.")
	testPrefetcherCheckGet(t, config.BlockCache(), rootPtr, rootDir,
		FinishedPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		rootDir.Children["c"].BlockPointer, fileC, FinishedPrefetch,
		TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		rootDir.Children["b"].BlockPointer, dirB, FinishedPrefetch,
		TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		rootDir.Children["a"].BlockPointer, fileA, FinishedPrefetch,
		TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		dirB.Children["d"].BlockPointer, dirBfileD, FinishedPrefetch,
		TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		dirBfileDptrs[0].BlockPointer, dirBfileDblock1, FinishedPrefetch,
		TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		dirBfileDptrs[1].BlockPointer, dirBfileDblock2, FinishedPrefetch,
		TransientEntry)

	block = &DirBlock{}
	ch = q.Request(context.Background(),
		defaultOnDemandRequestPriority, kmd, rootPtr, block, TransientEntry)
	// We don't need to release the block this time because it should be cached
	// already.
	err = <-ch
	require.NoError(t, err)
	require.Equal(t, rootDir, block)

	prefetchSyncCh <- struct{}{}
	t.Log("Wait for prefetching to complete. This shouldn't hang.")
	select {
	case <-q.Prefetcher().Shutdown():
	case <-time.After(time.Second):
		t.Fatal("Prefetching hung.")
	}

	testPrefetcherCheckGet(t, config.BlockCache(), rootPtr, rootDir,
		FinishedPrefetch, TransientEntry)
}

func TestPrefetcherMultiLevelIndirectFile(t *testing.T) {
	t.Log("Test multi-level indirect file block prefetching.")
	q, bg, config := initPrefetcherTest(t)
	defer shutdownPrefetcherTest(q)
	prefetchSyncCh := make(chan struct{})
	q.TogglePrefetcher(context.Background(), true, prefetchSyncCh)
	prefetchSyncCh <- struct{}{}

	t.Log("Initialize an indirect file block pointing to 2 file data blocks.")
	ptrs := []IndirectFilePtr{
		makeFakeIndirectFilePtr(t, 0),
		makeFakeIndirectFilePtr(t, 150),
	}
	rootPtr := makeRandomBlockPointer(t)
	rootBlock := &FileBlock{IPtrs: ptrs}
	rootBlock.IsInd = true
	indBlock1 := &FileBlock{IPtrs: []IndirectFilePtr{
		makeFakeIndirectFilePtr(t, 10),
		makeFakeIndirectFilePtr(t, 20),
	}}
	indBlock1.IsInd = true
	indBlock2 := &FileBlock{IPtrs: []IndirectFilePtr{
		makeFakeIndirectFilePtr(t, 30),
		makeFakeIndirectFilePtr(t, 40),
	}}
	indBlock2.IsInd = true
	indBlock11 := makeFakeFileBlock(t, true)
	indBlock12 := makeFakeFileBlock(t, true)
	indBlock21 := makeFakeFileBlock(t, true)
	indBlock22 := makeFakeFileBlock(t, true)

	_, continueChRootBlock := bg.setBlockToReturn(rootPtr, rootBlock)
	_, continueChIndBlock1 :=
		bg.setBlockToReturn(ptrs[0].BlockPointer, indBlock1)
	_, continueChIndBlock2 :=
		bg.setBlockToReturn(ptrs[1].BlockPointer, indBlock2)
	_, continueChIndBlock11 :=
		bg.setBlockToReturn(indBlock1.IPtrs[0].BlockPointer, indBlock11)
	_, continueChIndBlock12 :=
		bg.setBlockToReturn(indBlock1.IPtrs[1].BlockPointer, indBlock12)
	_, continueChIndBlock21 :=
		bg.setBlockToReturn(indBlock2.IPtrs[0].BlockPointer, indBlock21)
	_, continueChIndBlock22 :=
		bg.setBlockToReturn(indBlock2.IPtrs[1].BlockPointer, indBlock22)

	var block Block = &FileBlock{}
	ch := q.Request(context.Background(),
		defaultOnDemandRequestPriority, makeKMD(), rootPtr, block,
		TransientEntry)
	continueChRootBlock <- nil
	err := <-ch
	require.NoError(t, err)
	require.Equal(t, rootBlock, block)

	t.Log("Release the prefetched indirect blocks.")
	// Release after prefetching rootBlock
	prefetchSyncCh <- struct{}{}
	// Release 2 blocks
	continueChIndBlock1 <- nil
	continueChIndBlock2 <- nil

	t.Log("Wait for the prefetch to finish.")
	<-q.Prefetcher().Shutdown()
	q.TogglePrefetcher(context.Background(), true, prefetchSyncCh)
	prefetchSyncCh <- struct{}{}

	t.Log("Ensure that the prefetched blocks are in the cache.")
	testPrefetcherCheckGet(t, config.BlockCache(), rootPtr, rootBlock,
		TriggeredPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(), ptrs[0].BlockPointer,
		indBlock1, NoPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(), ptrs[1].BlockPointer,
		indBlock2, NoPrefetch, TransientEntry)

	t.Log("Fetch indirect block1 on-demand.")
	block = &FileBlock{}
	ch = q.Request(context.Background(), defaultOnDemandRequestPriority,
		makeKMD(), rootBlock.IPtrs[0].BlockPointer, block, TransientEntry)
	err = <-ch

	t.Log("Release the prefetch for indirect block1")
	// Release after prefetching block1
	prefetchSyncCh <- struct{}{}
	// Release 2 blocks
	continueChIndBlock11 <- nil
	continueChIndBlock12 <- nil

	t.Log("Wait for the prefetch to finish.")
	<-q.Prefetcher().Shutdown()
	q.TogglePrefetcher(context.Background(), true, prefetchSyncCh)
	prefetchSyncCh <- struct{}{}

	t.Log("Ensure that the prefetched blocks are in the cache.")
	testPrefetcherCheckGet(t, config.BlockCache(),
		indBlock1.IPtrs[0].BlockPointer, indBlock11, NoPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		indBlock1.IPtrs[1].BlockPointer, indBlock12, NoPrefetch, TransientEntry)

	t.Log("Fetch indirect block2 on-demand.")
	block = &FileBlock{}
	ch = q.Request(context.Background(), defaultOnDemandRequestPriority,
		makeKMD(), rootBlock.IPtrs[1].BlockPointer, block, TransientEntry)
	err = <-ch

	t.Log("Release the prefetch for indirect block2")
	// Release after prefetching block2
	prefetchSyncCh <- struct{}{}
	// Release 2 blocks
	continueChIndBlock21 <- nil
	continueChIndBlock22 <- nil

	t.Log("Wait for the prefetch to finish.")
	<-q.Prefetcher().Shutdown()

	t.Log("Ensure that the prefetched blocks are in the cache.")
	testPrefetcherCheckGet(t, config.BlockCache(),
		indBlock2.IPtrs[0].BlockPointer, indBlock21, NoPrefetch, TransientEntry)
	testPrefetcherCheckGet(t, config.BlockCache(),
		indBlock2.IPtrs[1].BlockPointer, indBlock22, NoPrefetch, TransientEntry)
}
