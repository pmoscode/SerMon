use std::path::Path;

use serde_json::json;

use super::*;
use crate::{BuildError, Config, Instance, Setup};

const DISTROS: [&str; 6] = [
    "debian-13",
    "ubuntu-24.04",
    "rockylinux-9",
    "almalinux-10",
    "fedora-44",
    "alpine-3.22",
];

fn fixture(distro: &str) -> ProcRoot {
    ProcRoot::new(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/discovery")
            .join(distro),
    )
}

fn listener(s: &str) -> Listener {
    let addr: std::net::SocketAddr = s.parse().unwrap();
    Listener {
        ip: addr.ip(),
        port: addr.port(),
    }
}

// The recorded hosts run nginx and (but Alpine) sshd: only the listening sockets count, the
// connections to the package mirrors (TIME_WAIT) don't.
#[test]
fn reads_the_listeners_of_every_distro() {
    for distro in DISTROS {
        let root = fixture(distro);
        let v4 = parse_tcp(&root.read_proc("net/tcp").unwrap());
        let v6 = parse_tcp(&root.read_proc("net/tcp6").unwrap());
        assert!(v4.contains(&listener("0.0.0.0:80")), "{distro}: {v4:?}");
        assert!(v6.contains(&listener("[::]:80")), "{distro}: {v6:?}");
        if distro != "alpine-3.22" {
            assert!(v4.contains(&listener("0.0.0.0:22")), "{distro}: {v4:?}");
        }
        assert!(
            v4.iter()
                .chain(&v6)
                .all(|l| l.ip.is_unspecified() || l.ip.is_loopback()),
            "{distro}: no connection taken: {v4:?}"
        );
    }
    // systemd-resolved on Fedora: two stub listeners on loopback and LLMNR.
    let fedora = parse_tcp(&fixture("fedora-44").read_proc("net/tcp").unwrap());
    for l in ["127.0.0.53:53", "127.0.0.54:53", "0.0.0.0:5355"] {
        assert!(fedora.contains(&listener(l)), "{l}");
    }
}

#[test]
fn decodes_the_addresses_in_host_byte_order() {
    let header = "  sl  local_address rem_address   st\n";
    let text = format!(
        "{header}   0: 0100007F:1F90 00000000:0000 0A x\n   1: 0A0200C0:01BB 00000000:0000 0A\n"
    );
    assert_eq!(
        parse_tcp(&text),
        [listener("127.0.0.1:8080"), listener("192.0.2.10:443")]
    );
    let v6 = format!(
        "{header}   0: 00000000000000000000000001000000:0050 0:0 0A\n   1: B80D01200000000000000000{}:0016 0:0 0A\n   2: 0000000000000000FFFF00000100007F:18EB 0:0 0A\n",
        "05000000"
    );
    assert_eq!(
        parse_tcp(&v6),
        [
            listener("[::1]:80"),
            listener("[2001:db8::5]:22"),
            listener("[::ffff:127.0.0.1]:6379"),
        ]
    );
}

#[test]
fn skips_what_is_no_listener() {
    let text = "header\n\
        0: 0100007F:0050 00000000:0000 01 established\n\
        1: 0100007F:0050\n\
        2: 0100007X:0050 00000000:0000 0A\n\
        3: 0100007F:050 00000000:0000 0A\n\
        4: 0100007F:00500 00000000:0000 0A\n\
        5: 0100007F 00000000:0000 0A\n\
        6: 01000007F:0050 00000000:0000 0A\n\
        7: +100007F:0050 00000000:0000 0A\n\
        8: 0100007F:0050 00000000:0000 0A\n\
        9: 0100007F:0050 00000000:0000 0A\n\
        0100007F:0051 00000000:0000 0A\n";
    assert_eq!(parse_tcp(text), [listener("127.0.0.1:80")], "once");
    assert!(
        parse_tcp("0: 0100007F:0050 00000000:0000 0A\n").is_empty(),
        "the header"
    );
}

#[test]
fn reads_the_listening_unix_sockets() {
    let rocky = parse_unix(&fixture("rockylinux-9").read_proc("net/unix").unwrap());
    assert!(
        rocky.contains(&"/run/systemd/private".to_owned()),
        "{rocky:?}"
    );
    assert!(
        !rocky.contains(&"/run/systemd/notify".to_owned()),
        "datagram, not listening"
    );
    let debian = parse_unix(&fixture("debian-13").read_proc("net/unix").unwrap());
    assert!(
        debian.contains(&"/run/dbus/system_bus_socket".to_owned()),
        "{debian:?}"
    );
    assert!(debian.iter().all(|p| p.starts_with('/')));
    let text = "Num RefCount Protocol Flags Type St Inode Path\n\
        0: 00000002 00000000 00010000 0001 01 1 @/tmp/abstract\n\
        0: 00000002 00000000 00010000 0001 01 2\n\
        0: 00000002 00000000 00010000 0001 03 3 /run/connected.sock\n\
        0: 00000002 00000000 00000000 0001 01 4 /run/client.sock\n\
        0: 00000002 00000000 0001000G 0001 01 5 /run/bad.sock\n\
        0: 00000002 00000000 00010000 0001 01 6 /run/with space.sock\n\
        0: 00000002 00000000 00010000 0001 01 7 /run/with space.sock\n";
    assert_eq!(parse_unix(text), ["/run/with space.sock"]);
    let long = format!(
        "h\n0: 2 0 00010000 0001 01 1 /{}\n",
        "x".repeat(MAX_SOCKET_PATH)
    );
    assert!(parse_unix(&long).is_empty(), "longer than sun_path");
}

#[test]
fn reads_the_facts_of_every_distro() {
    for distro in DISTROS {
        let facts = HostFacts::read(&fixture(distro), ["nginx.service".to_owned()]);
        assert!(facts.runs("nginx"), "{distro}: {:?}", facts.processes);
        assert!(!facts.runs("ngin"));
        assert!(facts.listener(80).is_some(), "{distro}");
        assert!(facts.listener(81).is_none(), "{distro}");
        assert!(facts.has_unit("nginx.service"));
        if distro != "alpine-3.22" {
            assert!(
                facts.runs("sshd") && facts.runs("systemd"),
                "{distro}: {:?}",
                facts.processes
            );
            assert!(facts.has_socket("/run/systemd/private"), "{distro}");
        }
    }
    let empty = HostFacts::read(&ProcRoot::new("/nonexistent"), Vec::new());
    assert_eq!(empty, HostFacts::default(), "nothing to read is no error");
}

#[test]
fn the_facts_are_bounded() {
    let dir = tempfile::tempdir().unwrap();
    for pid in 1..=MAX_FACTS + 10 {
        std::fs::create_dir_all(dir.path().join(format!("proc/{pid}"))).unwrap();
        std::fs::write(
            dir.path().join(format!("proc/{pid}/comm")),
            format!("p{pid}\n"),
        )
        .unwrap();
    }
    let units = (0..MAX_FACTS + 10).map(|i| format!("u{i}.service"));
    let facts = HostFacts::read(&ProcRoot::new(dir.path()), units);
    assert_eq!(facts.processes.len(), MAX_FACTS);
    assert!(facts.runs("p1"));
    assert_eq!(facts.units.len(), MAX_FACTS);
    let many: String = std::iter::once("header\n".to_owned())
        .chain((0..=MAX_FACTS).map(|p| format!("0: 0100007F:{p:04X} 00000000:0000 0A\n")))
        .collect();
    assert_eq!(parse_tcp(&many).len(), MAX_FACTS);
}

#[test]
fn only_numeric_entries_are_processes() {
    let dir = tempfile::tempdir().unwrap();
    for (entry, comm) in [("self", "self\n"), ("1", "init\n"), ("2", "\n")] {
        std::fs::create_dir_all(dir.path().join("proc").join(entry)).unwrap();
        std::fs::write(dir.path().join("proc").join(entry).join("comm"), comm).unwrap();
    }
    let facts = HostFacts::read(&ProcRoot::new(dir.path()), Vec::new());
    assert_eq!(facts.processes, ["init".to_owned()].into());
}

#[test]
fn suggests_urls_on_a_listener() {
    assert_eq!(listener("0.0.0.0:80").http_url("/x"), "http://127.0.0.1/x");
    assert_eq!(listener("[::]:8080").http_url("/x"), "http://[::1]:8080/x");
}

#[test]
fn a_listener_on_all_addresses_is_reached_on_loopback() {
    assert_eq!(
        listener("0.0.0.0:80").reach(),
        "127.0.0.1".parse::<IpAddr>().unwrap()
    );
    assert_eq!(
        listener("[::]:80").reach(),
        "::1".parse::<IpAddr>().unwrap()
    );
    assert_eq!(
        listener("192.0.2.1:80").reach(),
        "192.0.2.1".parse::<IpAddr>().unwrap()
    );
}

/// A module that finds what it is told to.
struct Fake {
    id: &'static str,
    found: fn(&HostFacts) -> Option<Discovered>,
}

impl Module for Fake {
    fn id(&self) -> &'static str {
        self.id
    }
    fn schema(&self) -> &'static str {
        "{}"
    }
    fn build(&self, _: &Config, _: &Setup<'_>) -> Result<Box<dyn Instance>, BuildError> {
        Err(BuildError::Invalid("a fake".into()))
    }
    fn discover(&self, facts: &HostFacts) -> Option<Discovered> {
        (self.found)(facts)
    }
}

fn fake(id: &'static str, found: fn(&HostFacts) -> Option<Discovered>) -> Arc<dyn Module> {
    Arc::new(Fake { id, found })
}

fn nginx(facts: &HostFacts) -> Option<Discovered> {
    let mut found = Discovered::evidence(facts, &["nginx"], &["nginx.service"], &[80, 443])?;
    found.suggested = facts
        .listener(80)
        .map(|l| json!({ "url": format!("http://{}/nginx_status", l.reach()) }));
    Some(found)
}

// ADR 0039: a hint names the module and its reasons one by one, with a suggestion for the
// form; a module without evidence gives none.
#[test]
fn a_module_suggests_itself_for_what_it_finds() {
    let modules = [fake("nginx", nginx), fake("redis", |_| None)];
    for distro in DISTROS {
        let units: Vec<String> = if distro == "alpine-3.22" {
            Vec::new()
        } else {
            vec!["nginx.service".into()]
        };
        let facts = HostFacts::read(&fixture(distro), units);
        let hints = hints(&modules, &facts);
        assert_eq!(hints.len(), 1, "{distro}");
        let expected: &[&str] = if distro == "alpine-3.22" {
            &["process:nginx", "port:80"]
        } else {
            &["process:nginx", "unit:nginx.service", "port:80"]
        };
        assert_eq!(hints[0].module, "nginx");
        assert_eq!(hints[0].reasons, expected, "{distro}");
        assert_eq!(
            hints[0].suggested_config_json,
            r#"{"url":"http://127.0.0.1/nginx_status"}"#
        );
    }
}

#[test]
fn a_port_alone_is_no_evidence() {
    let facts = HostFacts {
        listeners: [listener("0.0.0.0:80")].into(),
        ..HostFacts::default()
    };
    assert!(Discovered::evidence(&facts, &["nginx"], &["nginx.service"], &[80]).is_none());
    let unit = HostFacts {
        units: ["nginx.service".to_owned()].into(),
        ..facts
    };
    let found = Discovered::evidence(&unit, &["nginx"], &["nginx.service"], &[80, 443]).unwrap();
    assert_eq!(
        found.reasons,
        [Reason::Unit("nginx.service"), Reason::Port(80)]
    );
}

// The protocol's bounds hold whatever a module returns; a panic stays with its module.
#[test]
fn hints_stay_in_the_protocol_bounds() {
    let facts = HostFacts::default();
    let modules = [
        fake("crash", |_| panic!("a bug in a module")),
        fake("escaped", |_| {
            Some(Discovered {
                reasons: vec![Reason::Unit("app@web\\x2d01.service")],
                suggested: None,
            })
        }),
        fake("many", |_| {
            Some(Discovered {
                reasons: (1..=20)
                    .map(Reason::Port)
                    .chain([Reason::Port(1)])
                    .collect(),
                suggested: Some(json!(["not", "an", "object"])),
            })
        }),
        fake("big", |_| {
            Some(Discovered {
                reasons: vec![Reason::Process("big"), Reason::Process("big")],
                suggested: Some(json!({ "x": "y".repeat(MAX_SUGGESTED) })),
            })
        }),
        fake("long", |_| {
            Some(Discovered {
                reasons: vec![
                    Reason::Unit(Box::leak("u".repeat(MAX_REASON_VALUE + 1).into_boxed_str())),
                    Reason::Process(""),
                    Reason::Process("ok"),
                ],
                suggested: None,
            })
        }),
    ];
    let hints = hints(&modules, &facts);
    let ids: Vec<&str> = hints.iter().map(|h| h.module.as_str()).collect();
    assert_eq!(
        ids,
        ["big", "long", "many"],
        "sorted; no reason left is no hint"
    );
    assert_eq!(hints[0].reasons, ["process:big"], "once");
    assert_eq!(hints[0].suggested_config_json, "", "too long");
    assert_eq!(hints[1].reasons, ["process:ok"]);
    assert_eq!(hints[2].reasons.len(), MAX_REASONS);
    assert_eq!(hints[2].suggested_config_json, "", "no object");

    let ids: Vec<&'static str> = (0..40)
        .map(|i| &*Box::leak(format!("m{i:02}").into_boxed_str()))
        .collect();
    let modules: Vec<Arc<dyn Module>> = ids
        .iter()
        .rev()
        .map(|id| {
            fake(id, |_| {
                Some(Discovered {
                    reasons: vec![Reason::Port(1)],
                    suggested: Some(json!({})),
                })
            })
        })
        .chain([fake("m00", |_| {
            Some(Discovered {
                reasons: vec![Reason::Port(2)],
                suggested: None,
            })
        })])
        .collect();
    let hints = super::hints(&modules, &facts);
    assert_eq!(hints.len(), MAX_HINTS);
    assert_eq!(hints[0].module, "m00");
    assert_eq!(hints[0].reasons, ["port:1"], "a module once");
    assert_eq!(hints[0].suggested_config_json, "{}");
    assert_eq!(hints[MAX_HINTS - 1].module, "m31");
}

#[test]
fn only_the_protocols_reasons_pass() {
    for ok in [
        "process:nginx",
        "port:80",
        "unit:redis-server@main.service",
        "unit:a:b",
    ] {
        assert!(valid_reason(ok), "{ok}");
    }
    for bad in [
        "process:",
        "pid:1",
        "nginx",
        "unit:a b",
        "process:ä",
        "unit:a/b",
    ] {
        assert!(!valid_reason(bad), "{bad}");
    }
}
