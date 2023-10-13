// Copyright (c) 2020-2023 Blockwatch Data Inc.
// Author: alex@blockwatch.cc

package rpc

import (
	"context"
	"sync"
	"time"

	"blockwatch.cc/tzgo/tezos"
)

// WIP: interface may change
//
// TODO:
// - support multiple subscriptions (funcs) for the same op hash
// - support block subscriptions (to connect a BlockObserver for full blocks + reorgs)
// - support AdressObserver with address subscription filter
// - disable events/polling when no subscriber exists
// - handle reorgs (inclusion may switch to a different block hash)

type ObserverCallback func(tezos.BlockHash, int64, int, int, bool) bool

type observerSubscription struct {
	id      int
	cb      ObserverCallback
	oh      tezos.OpHash
	matched bool
}

type Observer struct {
	subs       map[int]*observerSubscription
	watched    map[tezos.OpHash]int
	recent     map[tezos.OpHash][3]int64
	seq        int
	once       sync.Once
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	c          *Client
	minDelay   time.Duration
	bestHash   tezos.BlockHash
	bestHeight int64
}

func NewObserver() *Observer {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Observer{
		subs:     make(map[int]*observerSubscription),
		watched:  make(map[tezos.OpHash]int),
		recent:   make(map[tezos.OpHash][3]int64),
		minDelay: tezos.DefaultParams.MinimalBlockDelay,
		ctx:      ctx,
		cancel:   cancel,
	}
	return m
}

func (m *Observer) WithDelay(minDelay time.Duration) *Observer {
	m.minDelay = minDelay
	return m
}

func (m *Observer) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel()
	m.subs = make(map[int]*observerSubscription)
	m.watched = make(map[tezos.OpHash]int)
	m.recent = make(map[tezos.OpHash][3]int64)
}

func (m *Observer) Subscribe(oh tezos.OpHash, cb ObserverCallback) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	seq := m.seq
	m.subs[seq] = &observerSubscription{
		id: seq,
		cb: cb,
		oh: oh,
	}
	m.watched[oh] = seq
	log.Debugf("monitor: %03d subscribed %s", seq, oh)
	if pos, ok := m.recent[oh]; ok {
		match := m.subs[seq]
		if remove := match.cb(m.bestHash, pos[0], int(pos[1]), int(pos[2]), false); remove {
			delete(m.subs, match.id)
		}
	}
	return seq
}

func (m *Observer) Unsubscribe(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, ok := m.subs[id]
	if ok {
		delete(m.watched, req.oh)
		delete(m.subs, id)
		log.Debugf("monitor: %03d unsubscribed %s", id, req.oh)
	}
}

func (m *Observer) Listen(cli *Client) {
	m.once.Do(func() {
		m.c = cli
		if m.c.Params != nil {
			m.minDelay = m.c.Params.MinimalBlockDelay
		}
		go m.listenBlocks()
	})
}

func (m *Observer) ListenMempool(cli *Client) {
	m.once.Do(func() {
		m.c = cli
		if m.c.Params != nil {
			m.minDelay = m.c.Params.MinimalBlockDelay
		}
		go m.listenMempool()
	})
}

func (m *Observer) listenMempool() {
	// TODO
}

func (m *Observer) listenBlocks() {
	var (
		mon *BlockHeaderMonitor
		// lastBlock int64
		useEvents bool = true
		firstLoop bool = true
	)
	defer func() {
		if mon != nil {
			mon.Close()
		}
	}()

	for {
		// handle close request
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		// (re)connect
		if mon == nil && useEvents {
			mon = NewBlockHeaderMonitor()
			if err := m.c.MonitorBlockHeader(m.ctx, mon); err != nil {
				mon.Close()
				mon = nil
				if ErrorStatus(err) == 404 {
					log.Debug("monitor: event mode unsupported, falling back to poll mode.")
					useEvents = false
				} else {
					// wait 5 sec, but also return on close
					select {
					case <-m.ctx.Done():
						return
					case <-time.After(5 * time.Second):
					}
				}
				continue
			}
		}

		var (
			headBlock  tezos.BlockHash
			headHeight int64
		)
		if mon != nil && useEvents && !firstLoop {
			// event mode: wait for next block message
			head, err := mon.Recv(m.ctx)
			// reconnect on error unless context was cancelled
			if err != nil {
				mon.Close()
				mon = nil
				continue
			}
			log.Debugf("monitor: new head %s", head.Hash)
			headBlock = head.Hash.Clone()
			headHeight = head.Level
		} else {
			// poll mode: check every 30sec
			head, err := m.c.GetTipHeader(m.ctx)
			if err != nil {
				// wait 5 sec, but also return on close
				select {
				case <-m.ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}
			headHeight = head.Level
			headBlock, err = m.c.GetBlockHash(m.ctx, BlockLevel(head.Level))
			if err != nil {
				log.Debugf("monitor: cannot fetch block hash at height %d: %v", head.Level, err)
				continue
			}
		}
		firstLoop = false

		// skip already processed blocks
		if headBlock.Equal(m.bestHash) && !useEvents {
			// wait minDelay/2 for late blocks
			if !useEvents {
				select {
				case <-m.ctx.Done():
					return
				case <-time.After(m.minDelay / 2):
				}
			}
			continue
		}
		log.Debugf("monitor: new block %d %s", headHeight, headBlock)

		// TODO: check for reorg and gaps

		// callback for all previous matches
		m.mu.Lock()
		for _, v := range m.subs {
			if v.matched {
				log.Debugf("monitor: signal n-th match for %d %s", v.id, v.oh)
				if remove := v.cb(headBlock, -1, -1, -1, false); remove {
					delete(m.subs, v.id)
				}
			}
		}
		m.mu.Unlock()

		// pull block ops and fan-out matches
		ohs, err := m.c.GetBlockOperationHashes(m.ctx, headBlock)
		if err != nil {
			log.Warnf("monitor: cannot fetch block ops: %v", err)
			continue
		}
		// clear recent op hashes
		for n := range m.recent {
			delete(m.recent, n)
		}
		m.mu.Lock()
		for l, list := range ohs {
			for n, h := range list {
				// keep as recent
				m.recent[h] = [3]int64{headHeight, int64(l), int64(n)}

				// match op hash against subs
				id, ok := m.watched[h]
				if !ok {
					log.Debugf("monitor: --- !! %s", h)
					continue
				}
				match, ok := m.subs[id]
				if !ok {
					log.Debugf("monitor: --- !! %s", h)
					continue
				}

				// cross check hash to guard against hash collisions
				if !match.oh.Equal(h) {
					log.Debugf("monitor: %03d != %s", id, h)
					continue
				}

				log.Debugf("monitor: matched %d %s", match.id, match.oh)

				// callback
				if remove := match.cb(headBlock, headHeight, l, n, false); remove {
					delete(m.subs, match.id)
				} else {
					match.matched = true
				}
			}
		}

		// update monitor state
		m.bestHash = headBlock
		m.bestHeight = headHeight
		m.mu.Unlock()

		// wait in poll mode
		if !useEvents {
			select {
			case <-m.ctx.Done():
				return
			case <-time.After(m.minDelay):
			}
		}
	}
}
