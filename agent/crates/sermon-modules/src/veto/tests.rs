use std::os::unix::fs::PermissionsExt;
use std::time::Duration;

use super::*;

/// A directory for the tests, its path resolved (as an admin writes it into `agent.toml`).
fn base() -> (tempfile::TempDir, PathBuf) {
    let dir = tempfile::tempdir().unwrap();
    let path = std::fs::canonicalize(dir.path()).unwrap();
    (dir, path)
}

#[test]
fn networks_hold_their_addresses() {
    let lo = Net::parse("127.0.0.0/8").unwrap();
    assert!(lo.contains("127.1.2.3".parse().unwrap()));
    assert!(!lo.contains("128.0.0.1".parse().unwrap()));
    assert!(
        lo.contains("::ffff:127.0.0.1".parse().unwrap()),
        "mapped IPv4"
    );
    let lan = Net::parse("192.0.2.0/25").unwrap();
    assert!(lan.contains("192.0.2.127".parse().unwrap()));
    assert!(!lan.contains("192.0.2.128".parse().unwrap()));
    let v6 = Net::parse("2001:db8::/32").unwrap();
    assert!(v6.contains("2001:db8:1::5".parse().unwrap()));
    assert!(!v6.contains("2001:db9::1".parse().unwrap()));
    assert!(Net::parse("::1").unwrap().contains("::1".parse().unwrap()));
    assert!(
        Net::parse("0.0.0.0/0")
            .unwrap()
            .contains("192.0.2.1".parse().unwrap())
    );
    for bad in ["127.0.0.0/33", "::/129", "host", "10.0.0.0/x"] {
        assert!(Net::parse(bad).is_err(), "{bad}");
    }
}

// ADR 0039: by default loopback only – no metadata service, no LAN.
#[test]
fn the_default_allows_loopback_only() {
    let v = Veto::default();
    assert!(v.address("127.0.0.1".parse().unwrap()).is_ok());
    assert!(v.address("::1".parse().unwrap()).is_ok());
    for other in [
        "169.254.169.254",
        "192.0.2.10",
        "0.0.0.0",
        "::",
        "2001:db8::1",
    ] {
        assert!(v.address(other.parse().unwrap()).is_err(), "{other}");
    }
}

#[tokio::test]
async fn a_name_is_allowed_only_with_every_address() {
    let v = Veto::default();
    let found = v.resolve("localhost", 80).await.unwrap();
    assert!(found.iter().all(|a| a.ip().is_loopback()));
    assert!(v.resolve("192.0.2.10", 80).await.is_err());
    assert_eq!(
        v.resolve("127.0.0.1", 80).await.unwrap(),
        ["127.0.0.1:80".parse().unwrap()]
    );
    assert_eq!(
        v.resolve("::1", 443).await.unwrap(),
        ["[::1]:443".parse().unwrap()]
    );
}

// M5-33: a name outside module_hosts is refused before any DNS query – even one that would
// resolve into module_targets.
#[tokio::test]
async fn no_lookup_outside_module_hosts() {
    let none = Veto {
        hosts: Vec::new(),
        ..Veto::default()
    };
    let why = none.resolve("localhost", 80).await.unwrap_err().0;
    assert!(why.contains("module_hosts"), "{why}");
    let all = Veto {
        hosts: vec!["*".into()],
        ..Veto::default()
    };
    assert!(all.resolve("localhost", 80).await.is_ok());
    // Forms getaddrinfo takes as addresses are names here.
    assert!(Veto::default().resolve("127.1", 80).await.is_err());
}

// M5-33: a refusal names no address a name resolved to, and a name that doesn't resolve reads
// the same as one that resolves elsewhere – no DNS reconnaissance through the reasons.
#[tokio::test]
async fn refusals_tell_the_server_nothing_new() {
    let v = Veto {
        hosts: vec!["*".into()],
        targets: vec![Net::parse("192.0.2.0/24").unwrap()],
        ..Veto::default()
    };
    let elsewhere = v.resolve("localhost", 80).await.unwrap_err().0;
    for address in ["127.0.0.1", "::1"] {
        assert!(!elsewhere.contains(address), "{elsewhere}");
    }
    let missing = v.resolve("nothing.invalid", 80).await.unwrap_err().0;
    assert_eq!(
        elsewhere.replace("localhost", "N"),
        missing.replace("nothing.invalid", "N")
    );
    let outside = Veto::default()
        .resolve("other.invalid", 80)
        .await
        .unwrap_err()
        .0;
    assert_eq!(
        outside.replace("other.invalid", "N"),
        missing.replace("nothing.invalid", "N")
    );
}

#[test]
fn host_patterns() {
    let v = Veto {
        hosts: vec!["db.example.org".into(), "*.svc.example.org".into()],
        ..Veto::default()
    };
    for name in [
        "db.example.org",
        "DB.Example.org.",
        "a.svc.example.org",
        "a.b.svc.example.org",
    ] {
        assert!(v.name_allowed(name), "{name}");
    }
    for name in [
        "svc.example.org",
        "xsvc.example.org",
        "example.org",
        "db.example.org.evil",
        "localhost",
    ] {
        assert!(!v.name_allowed(name), "{name}");
    }
    for good in ["localhost", "*.example.org", "*", "Db-1.example.org"] {
        assert!(host_pattern(good).is_ok(), "{good}");
    }
    assert_eq!(host_pattern("Db.Example.org").unwrap(), "db.example.org");
    for bad in [
        "",
        "*foo",
        "foo.*",
        "**.x",
        "a b",
        "a..b",
        "*.",
        "192.0.2.1/24",
        "a_b",
    ] {
        assert!(host_pattern(bad).is_err(), "{bad}");
    }
}

// M5-33: by default only the ports of the modules' services – no scan of loopback, no admin
// port of another service; `1-65535` allows every one.
#[tokio::test]
async fn ports_outside_module_ports_are_refused() {
    let v = Veto::default();
    for port in [80, 443, 3306, 5432, 6379, 8080, 9090, 9100, 9999] {
        assert!(v.resolve("127.0.0.1", port).await.is_ok(), "{port}");
    }
    for port in [22, 2375, 2379, 8500, 8778, 8983, 10255, 9089, 10000] {
        let why = v.resolve("127.0.0.1", port).await.unwrap_err().0;
        assert!(why.contains("module_ports"), "{port}: {why}");
    }
    let why = v.resolve("localhost", 22).await.unwrap_err().0;
    assert!(why.contains("module_ports"), "before the name: {why}");
    assert!(
        Veto::default()
            .any_port()
            .resolve("127.0.0.1", 22)
            .await
            .is_ok()
    );
    assert_eq!(Ports::parse("443").unwrap(), Ports::one(443));
    assert_eq!(Ports::parse("9090-9999").unwrap(), Ports::range(9090, 9999));
    assert!(Ports::parse("1-65535").unwrap().contains(65535));
    for bad in ["0", "70000", "9-1", "x", "1-", "-5"] {
        assert!(Ports::parse(bad).is_err(), "{bad}");
    }
}

#[test]
fn files_and_sockets_only_below_their_prefixes() {
    let (_dir, base) = base();
    let allowed = base.join("certs");
    std::fs::create_dir(&allowed).unwrap();
    std::fs::write(allowed.join("a.pem"), "").unwrap();
    std::fs::write(base.join("secret.key"), "").unwrap();
    std::os::unix::fs::symlink(base.join("secret.key"), allowed.join("link.pem")).unwrap();
    let v = Veto {
        file_prefixes: vec![allowed.clone()],
        sockets: vec![base.clone()],
        ..Veto::default()
    };
    assert!(v.file(&allowed.join("a.pem")).is_ok());
    let why = v.file(&allowed.join("link.pem")).unwrap_err().0;
    assert!(!why.contains("secret"), "where the link leads: {why}");
    assert!(v.file(&allowed.join("../secret.key")).is_err());
    assert!(v.file(Path::new("relative.pem")).is_err());
    assert!(v.file(Path::new("/etc/sermon/agent.toml")).is_err());
    assert!(v.file(&base.join("secret.key")).is_err());
    let sock = base.join("redis.sock");
    assert!(v.socket(&sock).is_ok());
    assert!(v.socket(&base.join("not/yet/there.sock")).is_ok());
    assert!(v.socket(&base.join("docker.sock")).is_err());
    assert!(v.socket(&base.join("podman.sock")).is_err());
    std::fs::write(base.join("docker.sock"), "").unwrap();
    std::os::unix::fs::symlink(base.join("docker.sock"), base.join("app.sock")).unwrap();
    assert!(v.socket(&base.join("app.sock")).is_err(), "a link to it");
    let run = Veto {
        sockets: vec!["/run".into()],
        ..Veto::default()
    };
    assert!(
        run.socket(Path::new("/run/sermon/helper.sock")).is_err(),
        "the root helper"
    );
    let docker = Veto {
        docker_socket: true,
        ..v.clone()
    };
    assert!(docker.socket(&base.join("docker.sock")).is_ok());
}

// M5-33: the sockets of other container runtimes and VM managers are root-equivalent too.
#[test]
fn every_container_runtime_needs_the_opt_in() {
    let (_dir, base) = base();
    let v = Veto {
        sockets: vec![base.clone()],
        ..Veto::default()
    };
    let docker = Veto {
        docker_socket: true,
        ..v.clone()
    };
    for path in [
        "crio.sock",
        "cri-dockerd.sock",
        "buildkitd.sock",
        "docker.raw.sock",
        "libvirt-sock",
        "containerd/s/0a1b2c",
        "lxd/unix.socket",
        "podman/api.sock",
    ] {
        let path = base.join(path);
        assert!(v.socket(&path).is_err(), "{}", path.display());
        assert!(docker.socket(&path).is_ok(), "{}", path.display());
    }
    std::fs::create_dir(base.join("app")).unwrap();
    std::os::unix::fs::symlink(base.join("crio.sock"), base.join("app/x.sock")).unwrap();
    assert!(
        v.socket(&base.join("app/x.sock")).is_err(),
        "a link to where it will be"
    );
    std::fs::write(base.join("crio.sock"), "").unwrap();
    assert!(v.socket(&base.join("app/x.sock")).is_err(), "a link to it");
    assert!(v.socket(&base.join("app/redis.sock")).is_ok());
}

// M5-33: no socket in a directory everyone may write to (`/run/lock`, `/tmp`): a local user
// could put a link to another socket in its place after the check.
#[test]
fn no_socket_where_everyone_may_write() {
    let (_dir, base) = base();
    let v = Veto {
        sockets: vec![base.clone()],
        ..Veto::default()
    };
    for (mode, ok) in [
        (0o755, true),
        (0o775, true),
        (0o777, false),
        (0o1777, false),
    ] {
        let dir = base.join(format!("d{mode:o}"));
        std::fs::create_dir(&dir).unwrap();
        std::fs::create_dir(dir.join("inner")).unwrap();
        std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(mode)).unwrap();
        for path in [
            dir.join("app.sock"),
            dir.join("inner/app.sock"),
            dir.join("gone/app.sock"),
        ] {
            assert_eq!(v.socket(&path).is_ok(), ok, "{mode:o}: {}", path.display());
        }
    }
    let shared = base.join("d777");
    let prefix = Veto {
        sockets: vec![shared.clone()],
        ..Veto::default()
    };
    assert!(
        prefix.socket(&shared.join("app.sock")).is_err(),
        "the prefix itself"
    );
}

#[test]
fn blocked_paths_stay_blocked() {
    let v = Veto {
        file_prefixes: vec!["/".into()],
        ..Veto::default()
    };
    for p in [
        "/proc/1/environ",
        "/etc/sermon/agent.toml",
        "/var/lib/sermon-agent/identity/key.pem",
    ] {
        assert!(v.file(Path::new(p)).is_err(), "{p}");
    }
    assert!(v.file(Path::new("/etc/hostname")).is_ok());
}

// M5-33: the agent blocks its own state directory wherever it lies, as written and where it
// leads.
#[test]
fn the_configured_state_dir_is_blocked() {
    let (_dir, base) = base();
    let state = base.join("state");
    std::fs::create_dir_all(state.join("identity")).unwrap();
    std::fs::write(state.join("identity/cert.pem"), "").unwrap();
    std::os::unix::fs::symlink(&state, base.join("link")).unwrap();
    let mut v = Veto {
        file_prefixes: vec![base.clone()],
        sockets: vec![base.clone()],
        ..Veto::default()
    };
    assert!(v.file(&state.join("identity/cert.pem")).is_ok());
    v.block(&base.join("link"));
    v.block(&base.join("link"));
    assert_eq!(v.blocked.len(), BLOCKED.len() + 1);
    for path in [
        state.join("identity/cert.pem"),
        base.join("link/identity/cert.pem"),
    ] {
        assert!(v.file(&path).is_err(), "{}", path.display());
        assert!(v.socket(&path).is_err(), "{}", path.display());
    }
    std::os::unix::fs::symlink(state.join("identity/cert.pem"), base.join("cert.pem")).unwrap();
    assert!(v.file(&base.join("cert.pem")).is_err(), "a link into it");
}

// M5-33: a module reads a file through `open`: a regular file below the prefixes only, without
// blocking on a FIFO, and the file opened is checked once more.
#[test]
fn open_checks_the_file_it_opened() {
    let (_dir, base) = base();
    let certs = base.join("certs");
    std::fs::create_dir(&certs).unwrap();
    std::fs::write(certs.join("a.pem"), "pem").unwrap();
    std::fs::write(base.join("secret.key"), "key").unwrap();
    let v = Veto {
        file_prefixes: vec![certs.clone()],
        ..Veto::default()
    };
    let mut text = String::new();
    std::io::Read::read_to_string(&mut v.open(&certs.join("a.pem")).unwrap(), &mut text).unwrap();
    assert_eq!(text, "pem");
    assert!(matches!(
        v.open(&certs.join("missing.pem")),
        Err(OpenError::Io(_))
    ));
    assert!(
        matches!(v.open(&certs), Err(OpenError::Io(_))),
        "a directory"
    );
    assert!(matches!(
        v.open(&base.join("secret.key")),
        Err(OpenError::Vetoed(_))
    ));
    let fifo = certs.join("fifo.pem");
    let made = std::process::Command::new("mkfifo")
        .arg(&fifo)
        .status()
        .unwrap();
    assert!(made.success());
    assert!(
        matches!(v.open(&fifo), Err(OpenError::Io(_))),
        "no FIFO, and no wait"
    );
    // A directory on the way swapped for a link between the check and the open.
    std::fs::create_dir(certs.join("sub")).unwrap();
    std::fs::write(certs.join("sub/b.pem"), "pem").unwrap();
    std::fs::create_dir(base.join("elsewhere")).unwrap();
    std::fs::write(base.join("elsewhere/b.pem"), "key").unwrap();
    let raced = v.open_after_check(&certs.join("sub/b.pem"), || {
        std::fs::rename(certs.join("sub"), base.join("sub.old")).unwrap();
        std::os::unix::fs::symlink(base.join("elsewhere"), certs.join("sub")).unwrap();
    });
    assert!(matches!(raced, Err(OpenError::Vetoed(_))), "{raced:?}");
    // A link swapped in between the check and the open: the file opened is outside.
    let outside = File::open(base.join("secret.key")).unwrap();
    let why = v
        .opened(&certs.join("a.pem"), &outside, &certs.join("a.pem"))
        .unwrap_err()
        .0;
    assert!(!why.contains("secret"), "{why}");
    let inside = File::open(certs.join("a.pem")).unwrap();
    assert!(
        v.opened(&certs.join("a.pem"), &inside, &certs.join("a.pem"))
            .is_ok()
    );
}

// V1-13: name lookups (getaddrinfo) can't be cancelled; a run that times out leaves its lookup
// running. At most a few run at once for all modules: a lookup waits for a place, and keeps it
// until it returns, also after the run gave up on it.
#[tokio::test]
async fn name_lookups_are_bounded() {
    static FEW: tokio::sync::Semaphore = tokio::sync::Semaphore::const_new(1);
    let (release, wait) = std::sync::mpsc::channel::<()>();
    let first = bounded(&FEW, move || {
        let _ = wait.recv();
        vec![SocketAddr::from(([127, 0, 0, 1], 80))]
    });
    assert!(
        tokio::time::timeout(Duration::from_millis(50), first)
            .await
            .is_err(),
        "the lookup blocks"
    );
    assert_eq!(FEW.available_permits(), 0, "its place is still taken");
    let second = bounded(&FEW, Vec::new);
    tokio::pin!(second);
    assert!(
        tokio::time::timeout(Duration::from_millis(50), &mut second)
            .await
            .is_err(),
        "waits for a place"
    );
    release.send(()).unwrap();
    assert_eq!(second.await, Vec::<SocketAddr>::new());
    assert_eq!(FEW.available_permits(), 1);
}

// V1-13: resolve takes its place among the lookups of all modules.
#[tokio::test]
async fn resolve_waits_for_a_place() {
    let all = LOOKUPS
        .acquire_many(u32::try_from(MAX_LOOKUPS).unwrap())
        .await
        .unwrap();
    let v = Veto::default();
    let lookup = v.resolve("localhost", 80);
    tokio::pin!(lookup);
    assert!(
        tokio::time::timeout(Duration::from_millis(50), &mut lookup)
            .await
            .is_err()
    );
    drop(all);
    assert!(lookup.await.is_ok());
}
