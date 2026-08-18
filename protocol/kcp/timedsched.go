package kcp

import (
	"container/heap"
	"runtime"
	"sync"
	"time"
)

// SystemTimedSched 是全部 KCP 会话共享的定时调度器
//
// 单逻辑 CPU 环境至少保留两个 worker 避免回调阻塞时饿死其他会话
var SystemTimedSched = NewTimedSched(max(runtime.NumCPU(), 2))

type timedFunc struct {
	execute func()
	ts      time.Time
}

// a heap for sorted timed function
type timedFuncHeap []timedFunc

func (h timedFuncHeap) Len() int            { return len(h) }
func (h timedFuncHeap) Less(i, j int) bool  { return h[i].ts.Before(h[j].ts) }
func (h timedFuncHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *timedFuncHeap) Push(x interface{}) { *h = append(*h, x.(timedFunc)) }
func (h *timedFuncHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	old[n-1] = timedFunc{}
	*h = old[0 : n-1]
	return x
}

// TimedSched represents the control struct for timed parallel scheduler
type TimedSched struct {
	// prepending tasks
	prependTasks    []timedFunc
	prependLock     sync.Mutex
	chPrependNotify chan struct{}

	// tasks will be distributed through chTask
	chTask chan timedFunc

	dieOnce sync.Once
	die     chan struct{}
}

// NewTimedSched creates a parallel-scheduler with given parallelization
func NewTimedSched(parallel int) *TimedSched {
	if parallel < 1 {
		parallel = 1
	}
	ts := new(TimedSched)
	ts.chTask = make(chan timedFunc)
	ts.die = make(chan struct{})
	ts.chPrependNotify = make(chan struct{}, 1)

	for i := 0; i < parallel; i++ {
		go ts.sched()
	}
	go ts.prepend()
	return ts
}

func (ts *TimedSched) sched() {
	var tasks timedFuncHeap
	timer := time.NewTimer(0)
	defer timer.Stop()
	drained := false
	for {
		select {
		case task := <-ts.chTask:
			now := time.Now()
			if now.After(task.ts) {
				// already delayed! execute immediately
				task.execute()
			} else {
				heap.Push(&tasks, task)
				// properly reset timer to trigger based on the top element
				stopped := timer.Stop()
				if !stopped && !drained {
					<-timer.C
				}
				timer.Reset(tasks[0].ts.Sub(now))
				drained = false
			}
		case now := <-timer.C:
			drained = true
			for tasks.Len() > 0 {
				if now.After(tasks[0].ts) {
					heap.Pop(&tasks).(timedFunc).execute()
				} else {
					timer.Reset(tasks[0].ts.Sub(now))
					drained = false
					break
				}
			}
		case <-ts.die:
			return
		}
	}
}

func (ts *TimedSched) prepend() {
	var tasks []timedFunc
	for {
		select {
		case <-ts.chPrependNotify:
			ts.prependLock.Lock()
			// 交换切片可缩短持锁时间并避免每轮复制待调度任务
			tasks, ts.prependTasks = ts.prependTasks, tasks[:0]
			ts.prependLock.Unlock()

			for k := range tasks {
				select {
				case ts.chTask <- tasks[k]:
					tasks[k] = timedFunc{}
				case <-ts.die:
					return
				}
			}
			tasks = tasks[:0]
		case <-ts.die:
			return
		}
	}
}

// Put a function 'f' awaiting to be executed at 'deadline'
func (ts *TimedSched) Put(f func(), deadline time.Time) {
	ts.prependLock.Lock()
	ts.prependTasks = append(ts.prependTasks, timedFunc{f, deadline})
	ts.prependLock.Unlock()

	select {
	case ts.chPrependNotify <- struct{}{}:
	default:
	}
}

// Close terminates this scheduler
func (ts *TimedSched) Close() { ts.dieOnce.Do(func() { close(ts.die) }) }
