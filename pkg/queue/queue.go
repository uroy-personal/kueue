/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package queue

import (
	"container/heap"
	"fmt"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Key is the key used to index the queue.
func Key(q *kueue.Queue) string {
	return fmt.Sprintf("%s/%s", q.Namespace, q.Name)
}

// queueKeyForWorkload is the key to find the queue for the workload in the index.
func queueKeyForWorkload(w *kueue.QueuedWorkload) string {
	return fmt.Sprintf("%s/%s", w.Namespace, w.Spec.QueueName)
}

// Queue is the internal implementation of kueue.Queue.
type Queue struct {
	ClusterQueue string

	items map[string]*workload.Info
}

func newQueue(q *kueue.Queue) *Queue {
	qImpl := &Queue{
		items: make(map[string]*workload.Info),
	}
	qImpl.update(q)
	return qImpl
}

func (q *Queue) update(apiQueue *kueue.Queue) {
	q.ClusterQueue = string(apiQueue.Spec.ClusterQueue)
}

func (q *Queue) AddOrUpdate(w *kueue.QueuedWorkload) {
	key := workload.Key(w)
	info := q.items[key]
	if info != nil {
		info.Obj = w
		return
	}
	q.items[key] = workload.NewInfo(w)
}

// ClusterQueue is the internal implementation of kueue.ClusterQueue that
// holds pending workloads.
type ClusterQueue struct {
	// QueueingStrategy indicates the queueing strategy of the workloads
	// across the queues in this ClusterQueue.
	QueueingStrategy kueue.QueueingStrategy

	heap heapImpl
}

func newClusterQueue(cq *kueue.ClusterQueue) (*ClusterQueue, error) {
	var less lessFunc

	switch cq.Spec.QueueingStrategy {
	case kueue.StrictFIFO:
		less = strictFIFO
	default:
		return nil, fmt.Errorf("invalid QueueingStrategy %q", cq.Spec.QueueingStrategy)
	}

	cqImpl := &ClusterQueue{
		heap: heapImpl{
			less:  less,
			items: make(map[string]*heapItem),
		},
	}
	cqImpl.update(cq)
	return cqImpl, nil
}

func (cq *ClusterQueue) update(apiCQ *kueue.ClusterQueue) {
	cq.QueueingStrategy = apiCQ.Spec.QueueingStrategy
}

func (cq *ClusterQueue) AddFromQueue(q *Queue) bool {
	added := false
	for _, w := range q.items {
		if cq.PushIfNotPresent(w) {
			added = true
		}
	}
	return added
}

func (cq *ClusterQueue) DeleteFromQueue(q *Queue) {
	for _, w := range q.items {
		cq.Delete(w.Obj)
	}
}

func (cq *ClusterQueue) PushIfNotPresent(info *workload.Info) bool {
	item := cq.heap.items[workload.Key(info.Obj)]
	if item != nil {
		return false
	}
	heap.Push(&cq.heap, *info)
	return true
}

func (cq *ClusterQueue) PushOrUpdate(w *kueue.QueuedWorkload) {
	item := cq.heap.items[workload.Key(w)]
	info := *workload.NewInfo(w)
	if item == nil {
		heap.Push(&cq.heap, info)
		return
	}
	item.obj = info
	heap.Fix(&cq.heap, item.index)
}

func (cq *ClusterQueue) Delete(w *kueue.QueuedWorkload) {
	item := cq.heap.items[workload.Key(w)]
	if item != nil {
		heap.Remove(&cq.heap, item.index)
	}
}

func (cq *ClusterQueue) Pop() *workload.Info {
	if cq.heap.Len() == 0 {
		return nil
	}
	w := heap.Pop(&cq.heap).(workload.Info)
	return &w
}

// strictFIFO is the function used by the clusterQueue heap algorithm to sort
// workloads. It sorts workloads based on their priority.
// When priorities are equal, it uses workloads.creationTimestamp.
func strictFIFO(a, b workload.Info) bool {
	p1 := utilpriority.Priority(a.Obj)
	p2 := utilpriority.Priority(b.Obj)

	if p1 != p2 {
		return p1 > p2
	}
	return a.Obj.CreationTimestamp.Before(&b.Obj.CreationTimestamp)
}

// heap.Interface implementation inspired by
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/internal/heap/heap.go

type lessFunc func(a, b workload.Info) bool

type heapItem struct {
	obj   workload.Info
	index int
}

type heapImpl struct {
	// items is a map from key of the objects to the objects and their index
	items map[string]*heapItem
	// heap keeps the keys of the objects ordered according to the heap invariant.
	heap []string
	less lessFunc
}

func (h *heapImpl) Len() int {
	return len(h.heap)
}

func (h *heapImpl) Less(i, j int) bool {
	a := h.items[h.heap[i]]
	b := h.items[h.heap[j]]
	return h.less(a.obj, b.obj)
}

func (h *heapImpl) Swap(i, j int) {
	h.heap[i], h.heap[j] = h.heap[j], h.heap[i]
	h.items[h.heap[i]].index = i
	h.items[h.heap[j]].index = j
}

func (h *heapImpl) Push(x interface{}) {
	wInfo := x.(workload.Info)
	key := workload.Key(wInfo.Obj)
	h.items[key] = &heapItem{
		obj:   wInfo,
		index: len(h.heap),
	}
	h.heap = append(h.heap, key)
}

func (h *heapImpl) Pop() interface{} {
	key := h.heap[len(h.heap)-1]
	h.heap = h.heap[:len(h.heap)-1]
	obj := h.items[key].obj
	delete(h.items, key)
	return obj
}
