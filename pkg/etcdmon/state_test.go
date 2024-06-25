package etcdmon

import (
	"fmt"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReconcile(t *testing.T) {
	state := State{
		Etcd:    map[uint64]StateTransition{},
		NewPods: map[string]time.Time{},
	}
	members := []*etcdserverpb.Member{
		{
			ID:       0x01,
			Name:     "etcd-1",
			PeerURLs: []string{"http://192.168.10.1:2380"},
		},
		{
			ID:        0x02,
			Name:      "etcd-2",
			PeerURLs:  []string{"http://192.168.10.2:2380"},
			IsLearner: true,
		},
		{
			ID:       0x03,
			Name:     "",
			PeerURLs: []string{"http://192.168.10.3:2380"},
		},
	}
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "etcd-1"},
			Status:     corev1.PodStatus{PodIP: "192.168.10.1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "etcd-2"},
			Status:     corev1.PodStatus{PodIP: "192.168.10.2"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "etcd-3"},
			Status:     corev1.PodStatus{PodIP: "192.168.10.3"},
		},
	}
	expectedStates := map[uint64]MemberState{0x01: Healthy, 0x02: Learner, 0x03: New}

	// Loop to ensure state is stable
	for i := 0; i < 2; i++ {
		err := state.reconcile(pods, members)
		if err != nil {
			t.Error(err)
		}
		if len(state.Etcd) != len(expectedStates) {
			t.Errorf("expected %d members in etcd state, found %d", len(state.Etcd), len(members))
		}
		for id, expectedState := range expectedStates {
			if state, found := state.Etcd[id]; found {
				if state.State != expectedState {
					t.Errorf("expected state %v for member 0x%x, got %v", expectedState, id, state)
				}
			} else {
				t.Errorf("member 0x%x not found in state", id)
			}
		}
		if len(state.NewPods) != 0 {
			t.Error(fmt.Errorf("expected %d new pods, found %d", 0, len(state.NewPods)))
		}
	}
}
