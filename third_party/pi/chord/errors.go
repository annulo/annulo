package chord

import "slices"

// RemoteServiceErrorCode is one of the failures that may cross a service
// boundary. The string values are the wire form.
type RemoteServiceErrorCode string

const (
	ServiceNotAllowed       RemoteServiceErrorCode = "service_not_allowed"
	ServiceNotFound         RemoteServiceErrorCode = "service_not_found"
	ServiceModeMismatch     RemoteServiceErrorCode = "service_mode_mismatch"
	ServiceMemberNotFound   RemoteServiceErrorCode = "service_member_not_found"
	ServiceMemberMismatch   RemoteServiceErrorCode = "service_member_mismatch"
	ServiceInstanceNotFound RemoteServiceErrorCode = "service_instance_not_found"
	ServiceStaleInstance    RemoteServiceErrorCode = "service_stale_instance"
	ServiceInvalidValue     RemoteServiceErrorCode = "service_invalid_value"
)

// remoteServiceErrorCodes is every code, in upstream's order. It is the
// readonly tuple REMOTE_SERVICE_ERROR_CODES: the set is fixed by the wire
// contract, so it is reached only through [RemoteServiceErrorCodes], which
// hands out a copy.
var remoteServiceErrorCodes = []RemoteServiceErrorCode{
	ServiceNotAllowed,
	ServiceNotFound,
	ServiceModeMismatch,
	ServiceMemberNotFound,
	ServiceMemberMismatch,
	ServiceInstanceNotFound,
	ServiceStaleInstance,
	ServiceInvalidValue,
}

// RemoteServiceErrorCodes returns every code, in upstream's order. The slice
// is the caller's to keep.
func RemoteServiceErrorCodes() []RemoteServiceErrorCode {
	return slices.Clone(remoteServiceErrorCodes)
}

// IsRemoteServiceErrorCode reports whether value, as received from a peer, is
// a known code.
func IsRemoteServiceErrorCode(value string) bool {
	return slices.Contains(remoteServiceErrorCodes, RemoteServiceErrorCode(value))
}

// RemoteServiceError is a service failure carrying a code a peer can act on.
type RemoteServiceError struct {
	Code    RemoteServiceErrorCode
	Message string
}

// NewRemoteServiceError builds a RemoteServiceError.
func NewRemoteServiceError(code RemoteServiceErrorCode, message string) *RemoteServiceError {
	return &RemoteServiceError{Code: code, Message: message}
}

// Error is the message alone, as Error.message is upstream; the code travels
// in Code.
func (e *RemoteServiceError) Error() string { return e.Message }
