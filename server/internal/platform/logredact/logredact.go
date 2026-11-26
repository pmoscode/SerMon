// Package logredact keeps fields marked debug_redact out of the server's logs. protobuf-go
// ignores the option in String(), and encoding/json shows every exported field of a generated
// message: a message with a credential (ModuleSecrets, SetGroupModuleRequest) in a log call
// would print it in plaintext (M5-33).
package logredact

import (
	"log/slog"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// ReplaceAttr is a slog.HandlerOptions.ReplaceAttr: a protobuf message becomes its text form
// without the fields marked debug_redact, at every depth; other values stay as they are.
func ReplaceAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindAny {
		return a
	}
	msg, ok := a.Value.Any().(proto.Message)
	if !ok {
		return a
	}
	clean := proto.Clone(msg)
	redact(clean.ProtoReflect())
	return slog.String(a.Key, prototext.MarshalOptions{}.Format(clean))
}

// redact clears the redacted fields of m and of the messages in it.
func redact(m protoreflect.Message) {
	var hidden []protoreflect.FieldDescriptor
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if opts, ok := fd.Options().(*descriptorpb.FieldOptions); ok && opts.GetDebugRedact() {
			hidden = append(hidden, fd)
			return true
		}
		switch {
		case fd.IsList() && fd.Message() != nil:
			list := v.List()
			for i := range list.Len() {
				redact(list.Get(i).Message())
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				redact(mv.Message())
				return true
			})
		case fd.Message() != nil && !fd.IsMap():
			redact(v.Message())
		}
		return true
	})
	for _, fd := range hidden {
		m.Clear(fd)
	}
}
