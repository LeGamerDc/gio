package server

import (
	"sync"
	"time"
)

type txMsg struct {
	api  uint16
	data []byte
}

type txAggregator struct {
	mu     sync.Mutex
	queue  []txMsg
	window time.Duration
	bytes  int
	maxB   int
}

func newTxAggregator(window time.Duration, maxBytes int) *txAggregator {
	return &txAggregator{window: window, maxB: maxBytes}
}

func (t *txAggregator) add(api uint16, data []byte) (ready [][]txMsg) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queue = append(t.queue, txMsg{api: api, data: data})
	t.bytes += len(data)
	if t.bytes >= t.maxB || len(t.queue) >= 16 {
		ready = append(ready, t.queue)
		t.queue = nil
		t.bytes = 0
	}
	return
}

// 时间轮：每个 shard 一个 1ms ticker，扫描所有连接是否到期（简化，直接轮询当前队列）。

type timerWheel struct {
	interval time.Duration
	stopCh   chan struct{}
}

func newTimerWheel(interval time.Duration) *timerWheel {
	return &timerWheel{interval: interval, stopCh: make(chan struct{})}
}

func (tw *timerWheel) run(onTick func()) {
	tk := time.NewTicker(tw.interval)
	defer tk.Stop()
	for {
		select {
		case <-tk.C:
			onTick()
		case <-tw.stopCh:
			return
		}
	}
}

func (tw *timerWheel) stop() { close(tw.stopCh) }
