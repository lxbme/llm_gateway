package main

type StageName string

const (
	StageRequestReceived  StageName = "request_received"
	StageRequestDecoded   StageName = "request_decoded"
	StageBeforeUpstream   StageName = "before_upstream"
	StageStreamChunk      StageName = "stream_chunk"
	StageResponseComplete StageName = "response_complete"
)

type StageAction string

const (
	ActionContinue       StageAction = "continue"
	ActionReject         StageAction = "reject"
	ActionDirectResponse StageAction = "direct_response"
	ActionStopPipeline   StageAction = "stop_pipeline"
)

type StageResult struct {
	Action     StageAction
	StatusCode int
	Err        error
	Message    string
}

func (r StageResult) normalized() StageResult {
	if r.Action == "" {
		r.Action = ActionContinue
	}
	return r
}

type StageHandler interface {
	Name() string
	Stages() []StageName
	Handle(*GatewayContext) StageResult
}

type Pipeline struct {
	handlersByStage map[StageName][]StageHandler
}

func NewPipeline(handlers ...StageHandler) *Pipeline {
	handlersByStage := make(map[StageName][]StageHandler)
	for _, handler := range handlers {
		for _, stage := range handler.Stages() {
			handlersByStage[stage] = append(handlersByStage[stage], handler)
		}
	}
	return &Pipeline{handlersByStage: handlersByStage}
}

func (p *Pipeline) RunStage(stage StageName, gw *GatewayContext) (StageResult, bool) {
	handlers := p.handlersByStage[stage]
	if len(handlers) == 0 {
		return StageResult{Action: ActionContinue}, false
	}

	for _, handler := range handlers {
		result := handler.Handle(gw).normalized()
		switch result.Action {
		case ActionContinue:
			continue
		case ActionStopPipeline:
			return result, false
		case ActionReject, ActionDirectResponse:
			return result, true
		default:
			return result, false
		}
	}

	return StageResult{Action: ActionContinue}, false
}
