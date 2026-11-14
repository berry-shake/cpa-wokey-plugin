// Package wokey is the Wokey provider for cpa-provider-core: what the plugin
// knows about the Wokey API itself (endpoints, the model catalog, the
// account, key and issuing data it reports, the errors it maps its own way).
package wokey

// ProviderID is the CPA provider key and the auth file type.
const ProviderID = "wokey"

const (
	EndpointChat      = "/v1/chat/completions"
	EndpointResponses = "/v1/responses"
	EndpointMessages  = "/v1/messages"
	EndpointModels    = "/v1/models"

	EndpointBalance = "/v1/dashboard/balance"
	EndpointKeyInfo = "/v1/key"
)
