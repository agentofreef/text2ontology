package contracts

// ValidatorRejection is the structured error shape returned by
// services/backend-api/handler/handler_lakehouse_metric.go's validateIntentRemote
// when a metric PUT (incl. ?dryRun=true) fails. Reused by agent-server explore
// mode to prepend rejection context into the next LLM turn (G9 in
// .omc/plans/plan-explore-chat-redesign-final.md).
type ValidatorRejection struct {
	Code   string   `json:"code"`
	Error  string   `json:"error"`
	Errors []string `json:"errors,omitempty"`
}
