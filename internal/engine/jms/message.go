package jmsengine

const (
	descData      = 0x75
	descAMQPValue = 0x77
)

// encodeDataBody builds a minimal AMQP message: a single data section
// (descriptor 0x75) wrapping the payload as raw bytes — the most
// universally-understood body shape (Qpid JMS maps a JMS BytesMessage/
// TextMessage payload to exactly this). No header, properties, or
// annotations sections are emitted; a consumer needs none of them to read
// the body.
func encodeDataBody(payload string) []byte {
	e := &encoder{}
	e.writeDescriptor(descData)
	e.writeBinary([]byte(payload))
	return e.buf
}

// decodeMessageBody extracts a text payload from a transfer's raw message
// bytes. Real messages may carry header/delivery-annotations/message-
// annotations/properties/application-properties sections before the body;
// each is just another described value at the top level, so they're
// decoded and discarded until a body section (data or amqp-value) is
// found. Returns "" if no recognizable body section is present.
func decodeMessageBody(raw []byte) string {
	d := newDecoder(raw)
	for d.remaining() > 0 {
		v, err := d.readValue()
		if err != nil {
			return ""
		}
		desc, ok := v.(*described)
		if !ok {
			continue
		}
		switch desc.Descriptor {
		case descData:
			if b, ok := desc.Value.([]byte); ok {
				return string(b)
			}
			return ""
		case descAMQPValue:
			switch val := desc.Value.(type) {
			case string:
				return val
			case []byte:
				return string(val)
			default:
				return ""
			}
		default:
			// header, delivery-annotations, message-annotations,
			// properties, application-properties, footer — not needed to
			// find the body, already consumed by readValue above.
			continue
		}
	}
	return ""
}
