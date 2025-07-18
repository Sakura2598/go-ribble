// Copyright 2022 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package pathdb

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Sakura2598/go-ribble/common"
	"github.com/Sakura2598/go-ribble/core/rawdb"
	"github.com/Sakura2598/go-ribble/core/types"
	"github.com/Sakura2598/go-ribble/crypto"
	"github.com/Sakura2598/go-ribble/ethdb"
	"github.com/Sakura2598/go-ribble/log"
	"github.com/Sakura2598/go-ribble/params"
	"github.com/Sakura2598/go-ribble/trie/trienode"
	"github.com/Sakura2598/go-ribble/trie/triestate"
)

const (
	// defaultCleanSize is the default memory allowance of clean cache.
	defaultCleanSize = 16 * 1024 * 1024

	// maxBufferSize is the maximum memory allowance of node buffer.
	// Too large nodebuffer will cause the system to pause for a long
	// time when write happens. Also, the largest batch that pebble can
	// support is 4GB, node will panic if batch size exceeds this limit.
	maxBufferSize = 256 * 1024 * 1024

	// DefaultBufferSize is the default memory allowance of node buffer
	// that aggregates the writes from above until it's flushed into the
	// disk. It's meant to be used once the initial sync is finished.
	// Do not increase the buffer size arbitrarily, otherwise the system
	// pause time will increase when the database writes happen.
	DefaultBufferSize = 64 * 1024 * 1024
)

var (
	// maxDiffLayers is the maximum diff layers allowed in the layer tree.
	maxDiffLayers = 128
)

// layer is the interface implemented by all state layers which includes some
// public methods and some additional methods for internal usage.
type layer interface {
	// node retrieves the trie node with the node info. An error will be returned
	// if the read operation exits abnormally. Specifically, if the layer is
	// already stale.
	//
	// Note, no error will be returned if the requested node is not found in database.
	node(owner common.Hash, path []byte, depth int) ([]byte, common.Hash, *nodeLoc, error)

	// rootHash returns the root hash for which this layer was made.
	rootHash() common.Hash

	// stateID returns the associated state id of layer.
	stateID() uint64

	// parentLayer returns the subsequent layer of it, or nil if the disk was reached.
	parentLayer() layer

	// update creates a new layer on top of the existing layer diff tree with
	// the provided dirty trie nodes along with the state change set.
	//
	// Note, the maps are retained by the method to avoid copying everything.
	update(root common.Hash, id uint64, block uint64, nodes map[common.Hash]map[string]*trienode.Node, states *triestate.Set) *diffLayer

	// journal commits an entire diff hierarchy to disk into a single journal entry.
	// This is meant to be used during shutdown to persist the layer without
	// flattening everything down (bad for reorgs).
	journal(w io.Writer) error
}

// Config contains the settings for database.
type Config struct {
	StateHistory   uint64 // Number of recent blocks to maintain state history for
	CleanCacheSize int    // Maximum memory allowance (in bytes) for caching clean nodes
	DirtyCacheSize int    // Maximum memory allowance (in bytes) for caching dirty nodes
	ReadOnly       bool   // Flag whether the database is opened in read only mode.
}

// sanitize checks the provided user configurations and changes anything that's
// unreasonable or unworkable.
func (c *Config) sanitize() *Config {
	conf := *c
	if conf.DirtyCacheSize > maxBufferSize {
		log.Warn("Sanitizing invalid node buffer size", "provided", common.StorageSize(conf.DirtyCacheSize), "updated", common.StorageSize(maxBufferSize))
		conf.DirtyCacheSize = maxBufferSize
	}
	return &conf
}

// Defaults contains default settings for Ethereum mainnet.
var Defaults = &Config{
	StateHistory:   params.FullImmutabilityThreshold,
	CleanCacheSize: defaultCleanSize,
	DirtyCacheSize: DefaultBufferSize,
}

// ReadOnly is the config in order to open database in read only mode.
var ReadOnly = &Config{ReadOnly: true}

// Database is a multiple-layered structure for maintaining in-memory trie nodes.
// It consists of one persistent base layer backed by a key-value store, on top
// of which arbitrarily many in-memory diff layers are stacked. The memory diffs
// can form a tree with branching, but the disk layer is singleton and common to
// all. If a reorg goes deeper than the disk layer, a batch of reverse diffs can
// be applied to rollback. The deepest reorg that can be handled depends on the
// amount of state histories tracked in the disk.
//
// At most one readable and writable database can be opened at the same time in
// the whole system which ensures that only one database writer can operate disk
// state. Unexpected open operations can cause the system to panic.
type Database struct {
	// readOnly is the flag whether the mutation is allowed to be applied.
	// It will be set automatically when the database is journaled during
	// the shutdown to reject all following unexpected mutations.
	readOnly   bool                         // Flag if database is opened in read only mode
	waitSync   bool                         // Flag if database is deactivated due to initial state sync
	isVerkle   bool                         // Flag if database is used for verkle tree
	bufferSize int                          // Memory allowance (in bytes) for caching dirty nodes
	config     *Config                      // Configuration for database
	diskdb     ethdb.Database               // Persistent storage for matured trie nodes
	tree       *layerTree                   // The group for all known layers
	freezer    ethdb.ResettableAncientStore // Freezer for storing trie histories, nil possible in tests
	lock       sync.RWMutex                 // Lock to prevent mutations from happening at the same time
}

// New attempts to load an already existing layer from a persistent key-value
// store (with a number of memory layers from a journal). If the journal is not
// matched with the base persistent layer, all the recorded diff layers are discarded.
func New(diskdb ethdb.Database, config *Config, isVerkle bool) *Database {
	if config == nil {
		config = Defaults
	}
	config = config.sanitize()

	// Establish a dedicated database namespace tailored for verkle-specific
	// data, ensuring the isolation of both verkle and merkle tree data. It's
	// important to note that the introduction of a prefix won't lead to
	// substantial storage overhead, as the underlying database will efficiently
	// compress the shared key prefix.
	if isVerkle {
		diskdb = rawdb.NewTable(diskdb, string(rawdb.VerklePrefix))
	}
	db := &Database{
		readOnly:   config.ReadOnly,
		isVerkle:   isVerkle,
		bufferSize: config.DirtyCacheSize,
		config:     config,
		diskdb:     diskdb,
	}
	// Construct the layer tree by resolving the in-disk singleton state
	// and in-memory layer journal.
	db.tree = newLayerTree(db.loadLayers())

	// Repair the state history, which might not be aligned with the state
	// in the key-value store due to an unclean shutdown.
	if err := db.repairHistory(); err != nil {
		log.Crit("Failed to repair pathdb", "err", err)
	}
	// Disable database in case node is still in the initial state sync stage.
	if rawdb.ReadSnapSyncStatusFlag(diskdb) == rawdb.StateSyncRunning && !db.readOnly {
		if err := db.Disable(); err != nil {
			log.Crit("Failed to disable database", "err", err) // impossible to happen
		}
	}
	return db
}

// repairHistory truncates leftover state history objects, which may occur due
// to an unclean shutdown or other unexpected reasons.
func (db *Database) repairHistory() error {
	// Open the freezer for state history. This mechanism ensures that
	// only one database instance can be opened at a time to prevent
	// accidental mutation.
	ancient, err := db.diskdb.AncientDatadir()
	if err != nil {
		// TODO error out if ancient store is disabled. A tons of unit tests
		// disable the ancient store thus the error here will immediately fail
		// all of them. Fix the tests first.
		return nil
	}
	freezer, err := rawdb.NewStateFreezer(ancient, db.isVerkle, db.readOnly)
	if err != nil {
		log.Crit("Failed to open state history freezer", "err", err)
	}
	db.freezer = freezer

	// Reset the entire state histories if the trie database is not initialized
	// yet. This action is necessary because these state histories are not
	// expected to exist without an initialized trie database.
	id := db.tree.bottom().stateID()
	if id == 0 {
		frozen, err := db.freezer.Ancients()
		if err != nil {
			log.Crit("Failed to retrieve head of state history", "err", err)
		}
		if frozen != 0 {
			err := db.freezer.Reset()
			if err != nil {
				log.Crit("Failed to reset state histories", "err", err)
			}
			log.Info("Truncated extraneous state history")
		}
		return nil
	}
	// Truncate the extra state histories above in freezer in case it's not
	// aligned with the disk layer. It might happen after a unclean shutdown.
	pruned, err := truncateFromHead(db.diskdb, db.freezer, id)
	if err != nil {
		log.Crit("Failed to truncate extra state histories", "err", err)
	}
	if pruned != 0 {
		log.Warn("Truncated extra state histories", "number", pruned)
	}
	return nil
}

// Update adds a new layer into the tree, if that can be linked to an existing
// old parent. It is disallowed to insert a disk layer (the origin of all). Apart
// from that this function will flatten the extra diff layers at bottom into disk
// to only keep 128 diff layers in memory by default.
//
// The passed in maps(nodes, states) will be retained to avoid copying everything.
// Therefore, these maps must not be changed afterwards.
func (db *Database) Update(root common.Hash, parentRoot common.Hash, block uint64, nodes *trienode.MergedNodeSet, states *triestate.Set) error {
	// Hold the lock to prevent concurrent mutations.
	db.lock.Lock()
	defer db.lock.Unlock()

	// Short circuit if the mutation is not allowed.
	if err := db.modifyAllowed(); err != nil {
		return err
	}
	if err := db.tree.add(root, parentRoot, block, nodes, states); err != nil {
		return err
	}
	// Keep 128 diff layers in the memory, persistent layer is 129th.
	// - head layer is paired with HEAD state
	// - head-1 layer is paired with HEAD-1 state
	// - head-127 layer(bottom-most diff layer) is paired with HEAD-127 state
	// - head-128 layer(disk layer) is paired with HEAD-128 state
	return db.tree.cap(root, maxDiffLayers)
}

// Commit traverses downwards the layer tree from a specified layer with the
// provided state root and all the layers below are flattened downwards. It
// can be used alone and mostly for test purposes.
func (db *Database) Commit(root common.Hash, report bool) error {
	// Hold the lock to prevent concurrent mutations.
	db.lock.Lock()
	defer db.lock.Unlock()

	// Short circuit if the mutation is not allowed.
	if err := db.modifyAllowed(); err != nil {
		return err
	}
	return db.tree.cap(root, 0)
}

// Disable deactivates the database and invalidates all available state layers
// as stale to prevent access to the persistent state, which is in the syncing
// stage.
func (db *Database) Disable() error {
	db.lock.Lock()
	defer db.lock.Unlock()

	// Short circuit if the database is in read only mode.
	if db.readOnly {
		return errDatabaseReadOnly
	}
	// Prevent duplicated disable operation.
	if db.waitSync {
		log.Error("Reject duplicated disable operation")
		return nil
	}
	db.waitSync = true

	// Mark the disk layer as stale to prevent access to persistent state.
	db.tree.bottom().markStale()

	// Write the initial sync flag to persist it across restarts.
	rawdb.WriteSnapSyncStatusFlag(db.diskdb, rawdb.StateSyncRunning)
	log.Info("Disabled trie database due to state sync")
	return nil
}

// Enable activates database and resets the state tree with the provided persistent
// state root once the state sync is finished.
func (db *Database) Enable(root common.Hash) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	// Short circuit if the database is in read only mode.
	if db.readOnly {
		return errDatabaseReadOnly
	}
	// Ensure the provided state root matches the stored one.
	root = types.TrieRootHash(root)
	stored := types.EmptyRootHash
	if blob := rawdb.ReadAccountTrieNode(db.diskdb, nil); len(blob) > 0 {
		stored = crypto.Keccak256Hash(blob)
	}
	if stored != root {
		return fmt.Errorf("state root mismatch: stored %x, synced %x", stored, root)
	}
	// Drop the stale state journal in persistent database and
	// reset the persistent state id back to zero.
	batch := db.diskdb.NewBatch()
	rawdb.DeleteTrieJournal(batch)
	rawdb.WritePersistentStateID(batch, 0)
	if err := batch.Write(); err != nil {
		return err
	}
	// Clean up all state histories in freezer. Theoretically
	// all root->id mappings should be removed as well. Since
	// mappings can be huge and might take a while to clear
	// them, just leave them in disk and wait for overwriting.
	if db.freezer != nil {
		if err := db.freezer.Reset(); err != nil {
			return err
		}
	}
	// Re-construct a new disk layer backed by persistent state
	// with **empty clean cache and node buffer**.
	db.tree.reset(newDiskLayer(root, 0, db, nil, newNodeBuffer(db.bufferSize, nil, 0)))

	// Re-enable the database as the final step.
	db.waitSync = false
	rawdb.WriteSnapSyncStatusFlag(db.diskdb, rawdb.StateSyncFinished)
	log.Info("Rebuilt trie database", "root", root)
	return nil
}

// Recover rollbacks the database to a specified historical point.
// The state is supported as the rollback destination only if it's
// canonical state and the corresponding trie histories are existent.
func (db *Database) Recover(root common.Hash) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	// Short circuit if rollback operation is not supported.
	if err := db.modifyAllowed(); err != nil {
		return err
	}
	if db.freezer == nil {
		return errors.New("state rollback is non-supported")
	}
	// Short circuit if the target state is not recoverable.
	root = types.TrieRootHash(root)
	if !db.Recoverable(root) {
		return errStateUnrecoverable
	}
	// Apply the state histories upon the disk layer in order.
	var (
		start = time.Now()
		dl    = db.tree.bottom()
	)
	for dl.rootHash() != root {
		h, err := readHistory(db.freezer, dl.stateID())
		if err != nil {
			return err
		}
		dl, err = dl.revert(h)
		if err != nil {
			return err
		}
		// reset layer with newly created disk layer. It must be
		// done after each revert operation, otherwise the new
		// disk layer won't be accessible from outside.
		db.tree.reset(dl)
	}
	rawdb.DeleteTrieJournal(db.diskdb)
	_, err := truncateFromHead(db.diskdb, db.freezer, dl.stateID())
	if err != nil {
		return err
	}
	log.Debug("Recovered state", "root", root, "elapsed", common.PrettyDuration(time.Since(start)))
	return nil
}

// Recoverable returns the indicator if the specified state is recoverable.
func (db *Database) Recoverable(root common.Hash) bool {
	// Ensure the requested state is a known state.
	root = types.TrieRootHash(root)
	id := rawdb.ReadStateID(db.diskdb, root)
	if id == nil {
		return false
	}
	// Recoverable state must below the disk layer. The recoverable
	// state only refers the state that is currently not available,
	// but can be restored by applying state history.
	dl := db.tree.bottom()
	if *id >= dl.stateID() {
		return false
	}
	// This is a temporary workaround for the unavailability of the freezer in
	// dev mode. As a consequence, the Pathdb loses the ability for deep reorg
	// in certain cases.
	// TODO(rjl493456442): Implement the in-memory ancient store.
	if db.freezer == nil {
		return false
	}
	// Ensure the requested state is a canonical state and all state
	// histories in range [id+1, disklayer.ID] are present and complete.
	return checkHistories(db.freezer, *id+1, dl.stateID()-*id, func(m *meta) error {
		if m.parent != root {
			return errors.New("unexpected state history")
		}
		root = m.root
		return nil
	}) == nil
}

// Close closes the trie database and the held freezer.
func (db *Database) Close() error {
	db.lock.Lock()
	defer db.lock.Unlock()

	// Set the database to read-only mode to prevent all
	// following mutations.
	db.readOnly = true

	// Release the memory held by clean cache.
	db.tree.bottom().resetCache()

	// Close the attached state history freezer.
	if db.freezer == nil {
		return nil
	}
	return db.freezer.Close()
}

// Size returns the current storage size of the memory cache in front of the
// persistent database layer.
func (db *Database) Size() (diffs common.StorageSize, nodes common.StorageSize) {
	db.tree.forEach(func(layer layer) {
		if diff, ok := layer.(*diffLayer); ok {
			diffs += common.StorageSize(diff.memory)
		}
		if disk, ok := layer.(*diskLayer); ok {
			nodes += disk.size()
		}
	})
	return diffs, nodes
}

// Initialized returns an indicator if the state data is already
// initialized in path-based scheme.
func (db *Database) Initialized(genesisRoot common.Hash) bool {
	var inited bool
	db.tree.forEach(func(layer layer) {
		if layer.rootHash() != types.EmptyRootHash {
			inited = true
		}
	})
	if !inited {
		inited = rawdb.ReadSnapSyncStatusFlag(db.diskdb) != rawdb.StateSyncUnknown
	}
	return inited
}

// SetBufferSize sets the node buffer size to the provided value(in bytes).
func (db *Database) SetBufferSize(size int) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	if size > maxBufferSize {
		log.Info("Capped node buffer size", "provided", common.StorageSize(size), "adjusted", common.StorageSize(maxBufferSize))
		size = maxBufferSize
	}
	db.bufferSize = size
	return db.tree.bottom().setBufferSize(db.bufferSize)
}

// modifyAllowed returns the indicator if mutation is allowed. This function
// assumes the db.lock is already held.
func (db *Database) modifyAllowed() error {
	if db.readOnly {
		return errDatabaseReadOnly
	}
	if db.waitSync {
		return errDatabaseWaitSync
	}
	return nil
}

// AccountHistory inspects the account history within the specified range.
//
// Start: State ID of the first history object for the query. 0 implies the first
// available object is selected as the starting point.
//
// End: State ID of the last history for the query. 0 implies the last available
// object is selected as the ending point. Note end is included in the query.
func (db *Database) AccountHistory(address common.Address, start, end uint64) (*HistoryStats, error) {
	return accountHistory(db.freezer, address, start, end)
}

// StorageHistory inspects the storage history within the specified range.
//
// Start: State ID of the first history object for the query. 0 implies the first
// available object is selected as the starting point.
//
// End: State ID of the last history for the query. 0 implies the last available
// object is selected as the ending point. Note end is included in the query.
//
// Note, slot refers to the hash of the raw slot key.
func (db *Database) StorageHistory(address common.Address, slot common.Hash, start uint64, end uint64) (*HistoryStats, error) {
	return storageHistory(db.freezer, address, slot, start, end)
}

// HistoryRange returns the block numbers associated with earliest and latest
// state history in the local store.
func (db *Database) HistoryRange() (uint64, uint64, error) {
	return historyRange(db.freezer)
}
