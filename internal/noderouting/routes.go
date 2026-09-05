// Package noderouting compiles operator playback policy into an ordered list
// of legal transport shapes. Concrete node health and capacity selection stay
// in nodepool; protocol handlers consume this same catalog and ordering.
package noderouting

import (
	"fmt"
	"sort"

	"github.com/Silo-Server/silo-server/internal/config"
)

type Workload string

const (
	WorkloadDirectPlay     Workload = "direct_play"
	WorkloadRemux          Workload = "remux"
	WorkloadVideoTranscode Workload = "video_transcode"
)

type Delivery string

const (
	DeliveryDirect           Delivery = "direct"
	DeliveryProgressiveRemux Delivery = "progressive_remux"
	DeliveryHLSRemux         Delivery = "hls_remux"
	DeliveryHLSVideo         Delivery = "hls_video_transcode"
)

type Execution string

const (
	ExecutionNone      Execution = "none"
	ExecutionAPI       Execution = "api"
	ExecutionProxy     Execution = "proxy"
	ExecutionTranscode Execution = "transcode"
)

type Egress string

const (
	EgressAPI   Egress = "api"
	EgressProxy Egress = "proxy"
)

type Shape struct {
	ID        string
	Workload  Workload
	Delivery  Delivery
	Execution Execution
	Egress    Egress
}

const (
	ShapeProgressiveRemuxAPI            = "progressive_remux_api"
	ShapeProgressiveRemuxTranscodeProxy = "progressive_remux_transcode_proxy"
)

func (s Shape) NeedsTranscodeNode() bool { return s.Execution == ExecutionTranscode }
func (s Shape) NeedsProxyNode() bool     { return s.Egress == EgressProxy }

type Request struct {
	Workload     Workload
	Delivery     Delivery
	Policy       config.PlaybackRoutingPolicy
	ProxyAllowed bool
}

type RejectionReason string

const (
	RejectionPolicyUnsatisfied RejectionReason = "routing_policy_unsatisfied"
	RejectionClientUnsupported RejectionReason = "route_client_unsupported"
)

type Rejection struct {
	ShapeID string
	Reason  RejectionReason
}

type Result struct {
	Candidates []Shape
	Rejected   []Rejection
}

// Candidates returns every structurally legal, client-compatible route in
// policy order. An empty candidate list is a hard policy/client conflict;
// temporary node availability is deliberately not considered here.
func Candidates(request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	request.Policy = config.EffectivePlaybackRoutingPolicy(request.Policy)

	executionPreference, egressPreference := preferences(request.Workload, request.Policy)
	shapes := legalShapes(request.Workload, request.Delivery)
	result := Result{Candidates: make([]Shape, 0, len(shapes))}
	for _, shape := range shapes {
		if shape.Egress == EgressProxy && !request.ProxyAllowed {
			result.Rejected = append(result.Rejected, Rejection{ShapeID: shape.ID, Reason: RejectionClientUnsupported})
			continue
		}
		if violatesHardExecution(shape, executionPreference) || violatesHardEgress(shape, egressPreference) {
			result.Rejected = append(result.Rejected, Rejection{ShapeID: shape.ID, Reason: RejectionPolicyUnsatisfied})
			continue
		}
		result.Candidates = append(result.Candidates, shape)
	}

	sort.SliceStable(result.Candidates, func(i, j int) bool {
		left := rank(result.Candidates[i], executionPreference, egressPreference)
		right := rank(result.Candidates[j], executionPreference, egressPreference)
		if left.totalMisses != right.totalMisses {
			return left.totalMisses < right.totalMisses
		}
		if left.executionMiss != right.executionMiss {
			return left.executionMiss < right.executionMiss
		}
		if left.remoteHops != right.remoteHops {
			return left.remoteHops < right.remoteHops
		}
		return result.Candidates[i].ID < result.Candidates[j].ID
	})
	return result, nil
}

func validateRequest(request Request) error {
	switch request.Workload {
	case WorkloadDirectPlay:
		if request.Delivery != DeliveryDirect {
			return fmt.Errorf("direct_play requires direct delivery, got %q", request.Delivery)
		}
	case WorkloadRemux:
		if request.Delivery != DeliveryProgressiveRemux && request.Delivery != DeliveryHLSRemux {
			return fmt.Errorf("remux requires progressive_remux or hls_remux delivery, got %q", request.Delivery)
		}
	case WorkloadVideoTranscode:
		if request.Delivery != DeliveryHLSVideo {
			return fmt.Errorf("video_transcode requires hls_video_transcode delivery, got %q", request.Delivery)
		}
	default:
		return fmt.Errorf("unknown playback workload %q", request.Workload)
	}
	return nil
}

func legalShapes(workload Workload, delivery Delivery) []Shape {
	shape := func(id string, execution Execution, egress Egress) Shape {
		return Shape{ID: id, Workload: workload, Delivery: delivery, Execution: execution, Egress: egress}
	}
	switch delivery {
	case DeliveryDirect:
		return []Shape{
			shape("direct_api", ExecutionNone, EgressAPI),
			shape("direct_proxy", ExecutionNone, EgressProxy),
		}
	case DeliveryProgressiveRemux:
		return []Shape{
			shape(ShapeProgressiveRemuxAPI, ExecutionAPI, EgressAPI),
			shape("progressive_remux_proxy", ExecutionProxy, EgressProxy),
			shape(ShapeProgressiveRemuxTranscodeProxy, ExecutionTranscode, EgressProxy),
		}
	case DeliveryHLSRemux:
		return []Shape{
			shape("hls_remux_api", ExecutionAPI, EgressAPI),
			shape("hls_remux_transcode_api", ExecutionTranscode, EgressAPI),
			shape("hls_remux_transcode_proxy", ExecutionTranscode, EgressProxy),
		}
	case DeliveryHLSVideo:
		return []Shape{
			shape("hls_video_api", ExecutionAPI, EgressAPI),
			shape("hls_video_transcode_api", ExecutionTranscode, EgressAPI),
			shape("hls_video_transcode_proxy", ExecutionTranscode, EgressProxy),
		}
	default:
		return nil
	}
}

func preferences(workload Workload, policy config.PlaybackRoutingPolicy) (config.PlaybackExecutionPreference, config.PlaybackEgressPreference) {
	switch workload {
	case WorkloadDirectPlay:
		return "", policy.DirectPlayEgress
	case WorkloadRemux:
		return policy.RemuxExecution, policy.RemuxEgress
	case WorkloadVideoTranscode:
		return policy.VideoTranscodeExecution, policy.VideoTranscodeEgress
	default:
		return "", ""
	}
}

func isWorker(execution Execution) bool {
	return execution == ExecutionProxy || execution == ExecutionTranscode
}

func violatesHardExecution(shape Shape, preference config.PlaybackExecutionPreference) bool {
	switch preference {
	case config.PlaybackExecutionWorkerOnly:
		return !isWorker(shape.Execution)
	case config.PlaybackExecutionAPIOnly:
		return shape.Execution != ExecutionAPI
	default:
		return false
	}
}

func violatesHardEgress(shape Shape, preference config.PlaybackEgressPreference) bool {
	switch preference {
	case config.PlaybackEgressProxyOnly:
		return shape.Egress != EgressProxy
	case config.PlaybackEgressAPIOnly:
		return shape.Egress != EgressAPI
	default:
		return false
	}
}

type shapeRank struct {
	totalMisses   int
	executionMiss int
	remoteHops    int
}

func rank(shape Shape, execution config.PlaybackExecutionPreference, egress config.PlaybackEgressPreference) shapeRank {
	executionMiss := 0
	switch execution {
	case config.PlaybackExecutionPreferWorker:
		if !isWorker(shape.Execution) {
			executionMiss = 1
		}
	case config.PlaybackExecutionPreferTranscode:
		switch shape.Execution {
		case ExecutionTranscode:
			executionMiss = 0
		case ExecutionProxy:
			executionMiss = 1
		default:
			executionMiss = 2
		}
	case config.PlaybackExecutionPreferAPI:
		if shape.Execution != ExecutionAPI {
			executionMiss = 1
		}
	}
	egressMiss := 0
	switch egress {
	case config.PlaybackEgressPreferProxy:
		if shape.Egress != EgressProxy {
			egressMiss = 1
		}
	case config.PlaybackEgressPreferAPI:
		if shape.Egress != EgressAPI {
			egressMiss = 1
		}
	}
	return shapeRank{
		totalMisses:   executionMiss + egressMiss,
		executionMiss: executionMiss,
		remoteHops:    remoteHops(shape),
	}
}

func remoteHops(shape Shape) int {
	hops := 0
	if isWorker(shape.Execution) {
		hops++
	}
	if shape.Execution == ExecutionTranscode && shape.Egress != EgressAPI {
		hops++
	}
	return hops
}
