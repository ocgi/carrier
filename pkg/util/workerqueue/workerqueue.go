// Copyright 2021 The OCGI Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package workerqueue

import (
	"time"

	"github.com/pkg/errors"

	k8serror "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

// Handler  processing the work queue
type Handler func(string) error

// WorkerQueue is an opinionated queue + worker for use
// with controllers and related and processing Kubernetes watched
// events and synchronising resources
type WorkerQueue struct {
	syncHandler Handler
	queue       workqueue.RateLimitingInterface
}

// FastRateLimiter returns a rate limiter without exponential back-off, with specified maximum per-item retry delay.
func FastRateLimiter() workqueue.RateLimiter {
	const numFastRetries = 5
	const fastDelay = 20 * time.Millisecond  // first few retries up to 'numFastRetries' are fast
	const slowDelay = 500 * time.Millisecond // subsequent retries are slow

	return workqueue.NewItemFastSlowRateLimiter(fastDelay, slowDelay, numFastRetries)
}

// ServerSetRateLimiter returns a rate limiter without exponential back-off, with specified maximum per-item retry delay.
func ServerSetRateLimiter() workqueue.RateLimiter {
	const numFastRetries = 5
	const fastDelay = 1 * time.Second // first few retries up to 'numFastRetries' are fast
	const slowDelay = 5 * time.Second // subsequent retries are slow

	return workqueue.NewItemFastSlowRateLimiter(fastDelay, slowDelay, numFastRetries)
}

// NewWorkerQueue returns a new worker queue for a given name
func NewWorkerQueue(handler Handler, queueName string) *WorkerQueue {
	return NewWorkerQueueWithRateLimiter(handler, queueName, workqueue.DefaultControllerRateLimiter())
}

// NewWorkerQueueWithRateLimiter returns a new worker queue for a given name and a custom rate limiter.
func NewWorkerQueueWithRateLimiter(handler Handler, queueName string, rateLimiter workqueue.RateLimiter) *WorkerQueue {
	return &WorkerQueue{
		queue:       workqueue.NewNamedRateLimitingQueue(rateLimiter, queueName),
		syncHandler: handler,
	}
}

// Enqueue puts the name of the runtime.Object in the
// queue to be processed. If you need to send through an
// explicit key, use an cache.ExplicitKey
func (wq *WorkerQueue) Enqueue(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		err = errors.Wrap(err, "Error creating key for object")
		runtime.HandleError(err)
		return
	}
	wq.queue.AddRateLimited(key)
}

// EnqueueImmediately performs Enqueue but without rate-limiting.
// This should be used to continue partially completed work after giving other
// items in the queue a chance of running.
func (wq *WorkerQueue) EnqueueImmediately(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		err = errors.Wrap(err, "Error creating key for object")
		runtime.HandleError(err)
		return
	}
	wq.queue.Add(key)
}

// EnqueueAfter delays an enqueue operation by duration
func (wq *WorkerQueue) EnqueueAfter(obj interface{}, duration time.Duration) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		err = errors.Wrap(err, "Error creating key for object")
		runtime.HandleError(err)
		return
	}

	wq.queue.AddAfter(key, duration)
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (wq *WorkerQueue) runWorker() {
	for wq.processNextWorkItem() {
	}
}

// processNextWorkItem processes the next work item.
// pretty self explanatory :)
func (wq *WorkerQueue) processNextWorkItem() bool {
	obj, quit := wq.queue.Get()
	if quit {
		return false
	}
	defer wq.queue.Done(obj)

	var key string
	var ok bool
	if key, ok = obj.(string); !ok {
		runtime.HandleError(errors.Errorf("expected string in queue, but got %T", obj))
		// this is a bad entry, we don't want to reprocess
		wq.queue.Forget(obj)
		return true
	}

	if err := wq.syncHandler(key); err != nil {
		// Conflicts are expected, so only show them in debug operations.
		if k8serror.IsConflict(errors.Cause(err)) {
			klog.Warning(err)
		} else {
			runtime.HandleError(err)
		}

		// we don't forget here, because we want this to be retried via the queue
		wq.queue.AddRateLimited(obj)
		return true
	}

	wq.queue.Forget(obj)
	return true
}

// Run the WorkerQueue processing via the Handler. Will block until stop is closed.
// Runs a certain number workers to process the rate limited queue
func (wq *WorkerQueue) Run(workers int, stop <-chan struct{}) {
	for i := 0; i < workers; i++ {
		go wq.run(stop)
	}

	<-stop
	klog.Info("...shutting down workers")
	wq.queue.ShutDown()
}

func (wq *WorkerQueue) run(stop <-chan struct{}) {
	wait.Until(wq.runWorker, time.Second, stop)
}
