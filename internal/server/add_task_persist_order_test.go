package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAddTaskReturnsAcceptedBeforeTaskPersistenceCompletes pins the accepted-order
// contract of POST /add-task (upstream 6345e58, "fix(serve): defer accepted API
// task execution"): the handler registers the task and returns 202 immediately;
// task execution and finished-task persistence happen in the background and must
// never block the response.
//
// persistMu is the production serialisation point for bbdown-tasks.json writes
// (persistFinishedTasks). Holding it here is the Go equivalent of upstream's held
// _persistLock: a persistence write is in flight and cannot complete. The request
// runs in its own goroutine; if the handler ever waits for the task (or for its
// persistence) before writing the response, the 202 cannot arrive until the lock
// is released and the bounded wait below turns red.
//
// 变异验证：
//   - 把 handleAddTask 里的 go s.processTask(...) 改成同步调用 → 响应卡在被占用的
//     persistMu 上，5s 上限处变红；
//   - 在写 202 之前插一次 s.persistFinishedTasks() → 同样变红。
func TestAddTaskReturnsAcceptedBeforeTaskPersistenceCompletes(t *testing.T) {
	s := newLoopbackServer(t)
	h := s.buildHandler()

	// Simulate an in-flight persistence write: it holds the write gate for the
	// whole request, so a handler that waits for persistence cannot respond.
	s.persistMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.persistMu.Unlock()
			locked = false
		}
		// 202 之后任务是异步的：不等它收尾，t.TempDir 的清理会与落盘撞车。
		waitAddTaskDone(t, s)
	}()

	respCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		// 无法解析的 URL：后台任务不经网络就能走到终态（以及 defer 里的持久化）。
		respCh <- do(h, http.MethodPost, "http://127.0.0.1:23333/add-task",
			"127.0.0.1:23333", "", "application/json", `{"url":"zz-not-a-real-url"}`)
	}()

	var rec *httptest.ResponseRecorder
	select {
	case rec = <-respCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("/add-task 在任务持久化仍未完成时 5s 内没有返回 202：响应被后台任务/落盘阻塞（upstream 6345e58）")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /add-task = %d, want 202（持久化被占用时也必须先返回）", rec.Code)
	}
	var resp AddTaskResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(rec.Body.String())), &resp); err != nil {
		t.Fatalf("响应体不是 AddTaskResponse: %v (%q)", err, rec.Body.String())
	}

	// 响应到达时持久化仍然被占用：任务记录不可能存在。这才是被钉住的顺序——
	// 不只是「202 回得快」。
	if _, err := os.Stat(s.taskFile); !os.IsNotExist(err) {
		t.Fatalf("响应到达时任务记录已落盘（stat err=%v）：202 与持久化的先后顺序未被钉住", err)
	}

	// 放行持久化，并证明被接受的任务之后确实落盘了（提前 202 不等于跳过持久化）。
	s.persistMu.Unlock()
	locked = false
	waitAddTaskDone(t, s)

	if !waitFor(5*time.Second, func() bool {
		data, err := os.ReadFile(s.taskFile)
		return err == nil && strings.Contains(string(data), resp.TaskID)
	}) {
		t.Fatalf("被接受的任务 %s 未在 5s 内持久化到 %s", resp.TaskID, s.taskFile)
	}
}
