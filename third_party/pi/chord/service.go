package chord

import "strings"

// reservedServicePrefix is the namespace chord keeps for its own control
// services, such as $chord.service.
const reservedServicePrefix = "$chord."

// Service is the stable identity of one shared service contract. T is the
// contract type a provider implements and a consumer receives; it is carried
// only in the type so that a Service[Models] cannot be passed where a
// Service[Echo] is expected. Two definitions with the same ID and locality
// compare equal, which is what lets a Service key a map the way upstream's
// provider tables key on service.id.
type Service[T any] struct {
	id    string
	local bool
}

// ID is the service's stable identifier.
func (s Service[T]) ID() string { return s.id }

// Local reports whether the service is process-local: it accepts an
// unrestricted Go contract and is never published remotely.
func (s Service[T]) Local() bool { return s.local }

func (s Service[T]) String() string { return s.id }

// DefineService declares a remotable service. Its contract T must be
// expressible as strict JSON at the boundary — upstream checks that at compile
// time and Go cannot, so the wire layer checks values as they cross.
//
// A service is a declaration, normally a package-level var, so an invalid ID
// is a programming error and panics the way regexp.MustCompile does: it must
// not be empty and must not begin with $chord., which is reserved.
func DefineService[T any](id string) Service[T] {
	return Service[T]{id: checkServiceID(id)}
}

// DefineLocalService declares a process-local service; see [DefineService]
// for the ID rules.
func DefineLocalService[T any](id string) Service[T] {
	return Service[T]{id: checkServiceID(id), local: true}
}

func checkServiceID(id string) string {
	if id == "" {
		panic("chord: Service ID must not be empty")
	}
	if strings.HasPrefix(id, reservedServicePrefix) {
		panic("chord: Service IDs beginning with " + reservedServicePrefix + " are reserved")
	}
	return id
}
