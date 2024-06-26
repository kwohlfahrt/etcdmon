package etcdmon

import (
	"fmt"
	"net/url"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

type MemberState int

const (
	New MemberState = iota
	Learner
	Gone // New/Learner members who's pod disappear transition to Gone state, and can be removed immediately
	Healthy
	Dead // Healthy members transition to Dead state, and can be removed after a delay
)

type StateTransition struct {
	State      MemberState
	Transition time.Time
}

type State struct {
	Etcd    map[uint64]StateTransition
	NewPods map[string]time.Time
}

func (s *State) checkQuorum() error {
	numDead := 0
	numLive := 0
	for _, state := range s.Etcd {
		switch state.State {
		case Dead:
			numDead += 1
		case Healthy:
			numLive += 1
		}
	}
	if numDead >= numLive {
		return fmt.Errorf("quorum would be lost with %d dead members, doing nothing", numDead)
	}
	return nil
}

// If it returns non-nil, no change was made to the state
func (s *State) reconcile(pods []*corev1.Pod, members []*etcdserverpb.Member) error {
	memberHosts := make(map[uint64][]string, len(members))
	hostMember := make(map[string]*etcdserverpb.Member, len(members))
	nameMember := make(map[string]*etcdserverpb.Member, len(members))
	for _, member := range members {
		if member.Name != "" {
			klog.Infof("Found started etcd member: %s\n", member.Name)
			nameMember[member.Name] = member
		} else {
			klog.Infof("Found unstarted etcd member: 0x%x\n", member.ID)
			hosts := make([]string, 0, len(member.PeerURLs))
			for _, peerUrl := range member.PeerURLs {
				url, err := url.Parse(peerUrl)
				if err != nil {
					return err
				}
				hostMember[url.Hostname()] = member
				hosts = append(hosts, url.Hostname())
			}
			memberHosts[member.ID] = hosts
		}
	}

	podIPs := make(map[string]*corev1.Pod, len(pods))
	podNames := make(map[string]*corev1.Pod, len(pods))
	for _, pod := range pods {
		// TODO: On k8s 1.29, check for `PodReadyToStartContainers` instead
		if pod.Status.PodIP == "" {
			klog.V(2).Infof("Skipping pod %s because it has no IP", pod.Name)
			continue
		}
		klog.V(2).Infof("Found pod %s", pod.Name)
		podIPs[pod.Status.PodIP] = pod
		podNames[pod.Name] = pod
	}

	newMemberStates := make(map[uint64]StateTransition, len(members))
	for _, member := range members {
		var newMemberState MemberState
		switch {
		case member.Name == "":
			foundPod := false
			for _, host := range memberHosts[member.ID] {
				if _, found := podIPs[host]; found {
					newMemberState = New
					foundPod = true
				}
			}
			if !foundPod {
				newMemberState = Gone
			}
		case member.IsLearner:
			if _, found := podNames[member.Name]; found {
				newMemberState = Learner
			} else {
				newMemberState = Gone
			}
		default:
			if _, found := podNames[member.Name]; found {
				newMemberState = Healthy
			} else {
				newMemberState = Dead
			}
		}

		transition := time.Now()
		if currentState, found := s.Etcd[member.ID]; found {
			if currentState.State == newMemberState {
				transition = currentState.Transition
			}
		}
		newMemberStates[member.ID] = StateTransition{State: newMemberState, Transition: transition}
	}

	newNewPods := make(map[string]time.Time)
	for _, pod := range pods {
		if _, found := nameMember[pod.Name]; found {
			continue
		}
		if _, found := hostMember[pod.Status.PodIP]; found {
			continue
		}
		if _, found := s.NewPods[pod.Name]; found {
			continue
		}
		newNewPods[pod.Status.PodIP] = time.Now()
	}

	s.Etcd = newMemberStates
	s.NewPods = newNewPods

	return nil
}
