//! The generated protocol types encode and decode losslessly.

use prost::Message;
use sermon_transport::proto::agent::v1::{
    Architecture, Heartbeat, Hello, HostFacts, SessionRequest, session_request::Payload,
};

fn hello() -> SessionRequest {
    SessionRequest {
        payload: Some(Payload::Hello(Hello {
            protocol_version: 1,
            agent_version: "0.0.0".into(),
            host_facts: Some(HostFacts {
                hostname: "web-01".into(),
                machine_id: "0123456789abcdef0123456789abcdef".into(),
                os_id: "debian".into(),
                os_version: "13".into(),
                kernel_release: "6.12.0-1-amd64".into(),
                architecture: Architecture::Amd64.into(),
                cpu_count: 4,
                memory_bytes: 8 << 30,
            }),
            config_version: 0,
        })),
    }
}

#[test]
fn hello_roundtrips_through_the_wire_format() {
    let original = hello();

    let decoded = SessionRequest::decode(original.encode_to_vec().as_slice()).unwrap();

    assert_eq!(decoded, original);
}

#[test]
fn heartbeat_carries_a_timestamp() {
    let request = SessionRequest {
        payload: Some(Payload::Heartbeat(Heartbeat {
            sent_at: Some(prost_types::Timestamp {
                seconds: 1_795_000_000,
                nanos: 0,
            }),
        })),
    };

    let decoded = SessionRequest::decode(request.encode_to_vec().as_slice()).unwrap();

    assert_eq!(decoded, request);
}

#[test]
fn unknown_architecture_values_decode_without_panicking() {
    let facts = HostFacts {
        architecture: 99,
        ..HostFacts::default()
    };

    let decoded = HostFacts::decode(facts.encode_to_vec().as_slice()).unwrap();

    assert!(Architecture::try_from(decoded.architecture).is_err());
}
