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

#[test]
fn enroll_request_roundtrips_and_carries_no_identity() {
    use sermon_transport::proto::agent::v1::{EnrollRequest, EnrollResponse};

    let request = EnrollRequest {
        token: "sme_example".into(),
        csr: vec![0x30, 0x82, 0x01, 0x00],
        host_facts: match hello().payload {
            Some(Payload::Hello(h)) => h.host_facts,
            _ => None,
        },
        agent_version: "0.0.0".into(),
    };
    let decoded = EnrollRequest::decode(request.encode_to_vec().as_slice()).unwrap();
    assert_eq!(decoded, request);

    let response = EnrollResponse {
        node_id: "0193f5a0-0000-7000-8000-000000000001".into(),
        certificate: vec![1, 2, 3],
        intermediates: vec![vec![4], vec![5]],
        trust_anchor: vec![6],
        not_after: None,
        trust_anchors: vec![vec![6], vec![7]],
    };
    let decoded = EnrollResponse::decode(response.encode_to_vec().as_slice()).unwrap();
    assert_eq!(decoded, response);
}

#[test]
fn the_enrollment_client_is_generated() {
    // Compile-time check: the agent has a tonic client for EnrollmentService (M1-13).
    fn assert_client<T>() {}
    assert_client::<
        sermon_transport::proto::agent::v1::enrollment_service_client::EnrollmentServiceClient<
            tonic::transport::Channel,
        >,
    >();
}

#[test]
fn batch_roundtrips_and_carries_its_sequence_number() {
    use sermon_transport::proto::agent::v1::{Batch, MetricBatch, Sample, batch, sample};

    let request = SessionRequest {
        payload: Some(Payload::Batch(Batch {
            seq: 1_795_046_400_000_001,
            content: Some(batch::Content::Metrics(MetricBatch {
                collected_at: Some(prost_types::Timestamp {
                    seconds: 1_795_046_400,
                    nanos: 0,
                }),
                samples: vec![Sample {
                    metric: "sermon_cpu_utilization_ratio".into(),
                    labels: [("cpu".to_owned(), "0".to_owned())].into(),
                    value: Some(sample::Value::IntValue(250_000)),
                }],
            })),
        })),
    };

    let decoded = SessionRequest::decode(request.encode_to_vec().as_slice()).unwrap();

    assert_eq!(decoded, request);
}

// Security events (M2-06): a batch in the session stream, and an event kind an older server does
// not know decodes as an empty oneof instead of failing.
#[test]
fn security_events_roundtrip_in_a_batch() {
    use sermon_transport::proto::agent::v1::{
        Batch, SecurityEvent, SecurityEventBatch, SshAuthMethod, SshLoginFailed, SshRemote, batch,
        security_event, ssh_remote,
    };

    let request = SessionRequest {
        payload: Some(Payload::Batch(Batch {
            seq: 7,
            content: Some(batch::Content::SecurityEvents(SecurityEventBatch {
                events: vec![SecurityEvent {
                    occurred_at: Some(prost_types::Timestamp {
                        seconds: 1_795_046_400,
                        nanos: 776_631_000,
                    }),
                    event: Some(security_event::Event::SshLoginFailed(SshLoginFailed {
                        user: "jürgen müller".into(),
                        remote: Some(SshRemote {
                            address: Some(ssh_remote::Address::Ip("2001:db8::7".into())),
                            port: 22,
                        }),
                        method: i32::from(SshAuthMethod::Password),
                        invalid_user: true,
                    })),
                }],
            })),
        })),
    };
    let decoded = SessionRequest::decode(request.encode_to_vec().as_slice()).unwrap();
    assert_eq!(decoded, request);

    // Field 99 of SecurityEvent: a future event kind.
    let future = [0x0a, 0x00, 0x9a, 0x06, 0x00];
    let event = SecurityEvent::decode(future.as_slice()).unwrap();
    assert!(event.event.is_none() && event.occurred_at.is_some());
}
