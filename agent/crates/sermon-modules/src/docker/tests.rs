use std::path::{Path, PathBuf};
use std::time::Duration;

use sermon_core::Value as V;

use super::cgroups::{Runtime, id_of, scan};
use super::*;
use crate::Schema;

const WEB: &str = "d0c1000000000000000000000000000000000000000000000000000000000000";
const CLIENT: &str = "d0c2000000000000000000000000000000000000000000000000000000000000";
const DISK: &str = "d0c3000000000000000000000000000000000000000000000000000000000000";
const BUSY: &str = "90d1000000000000000000000000000000000000000000000000000000000000";
const IDLE: &str = "90d2000000000000000000000000000000000000000000000000000000000000";

fn fixtures() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/modules/docker")
}

/// The captured tree `src` (e.g. `docker/t0`) in a temporary root, each cgroup directory
/// renamed by `moves` (from, to below `sys/fs/cgroup`): the layouts of the systemd drivers.
fn tree(src: &str, moves: &[(&str, &str)]) -> tempfile::TempDir {
    let dir = tempfile::tempdir().unwrap();
    copy(&fixtures().join(src), dir.path());
    let cg = dir.path().join("sys/fs/cgroup");
    for (from, to) in moves {
        std::fs::create_dir_all(cg.join(to).parent().unwrap()).unwrap();
        std::fs::rename(cg.join(from), cg.join(to)).unwrap();
    }
    dir
}

fn copy(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let target = to.join(entry.file_name());
        if entry.file_type().unwrap().is_dir() {
            copy(&entry.path(), &target);
        } else {
            std::fs::copy(entry.path(), target).unwrap();
        }
    }
}

/// `metric` of the container `id` (short ID).
fn value(samples: &[Sample], metric: &str, id: &str) -> Option<V> {
    samples
        .iter()
        .find(|s| {
            s.metric == metric
                && s.labels
                    .iter()
                    .any(|(k, v)| *k == "container" && v == &id[..12])
        })
        .map(|s| s.value)
}

fn total(samples: &[Sample], metric: &str) -> V {
    samples.iter().find(|s| s.metric == metric).unwrap().value
}

async fn collect(c: &mut Containers) -> Vec<Sample> {
    c.collect(&Run::default()).await.unwrap()
}

#[test]
fn its_schema_is_in_the_subset() {
    let schema = Schema::parse(SCHEMA).unwrap();
    assert_eq!(schema.validate("{}", &[]).unwrap()["max_containers"], 20);
    assert!(schema.validate(r#"{"max_containers":29}"#, &[]).is_err());
    assert_eq!(MAX_CONTAINERS, 28);
}

#[test]
fn knows_the_cgroups_of_docker_and_podman() {
    let id = WEB;
    assert_eq!(id_of(id), Some((Runtime::Docker, id)));
    assert_eq!(
        id_of(&format!("docker-{id}.scope")),
        Some((Runtime::Docker, id))
    );
    assert_eq!(
        id_of(&format!("libpod-{id}.scope")),
        Some((Runtime::Podman, id))
    );
    assert_eq!(id_of(&format!("libpod-{id}")), Some((Runtime::Podman, id)));
    for other in [
        format!("libpod-conmon-{id}.scope"),
        format!("docker-{id}"),
        format!("docker-{}.scope", &id[1..]),
        id.to_uppercase(),
        "docker.service".to_owned(),
        "buildx".to_owned(),
        format!("{id}0"),
    ] {
        assert_eq!(id_of(&other), None, "{other}");
    }
}

// Docker with the cgroupfs driver (as captured): a web server, a client and a container writing
// to disk; rates from the second run on.
#[tokio::test(start_paused = true)]
async fn reads_dockers_containers_and_their_rates() {
    let (t0, t1) = (tree("docker/t0", &[]), tree("docker/t1", &[]));
    let mut c = Containers::new(ProcRoot::new(t0.path()), 20);
    let first = collect(&mut c).await;
    assert_eq!(total(&first, "sermon_docker_containers"), V::Int(3));
    assert_eq!(total(&first, "sermon_docker_truncated"), V::Int(0));
    assert_eq!(
        value(&first, "sermon_docker_cpu_utilization_ratio", WEB),
        None,
        "no rate yet"
    );
    let web = first
        .iter()
        .find(|s| s.metric == "sermon_docker_processes")
        .unwrap();
    assert!(web.labels.contains(&("runtime", "docker".to_owned())));

    tokio::time::advance(Duration::from_secs(5)).await;
    c.root = ProcRoot::new(t1.path());
    let second = collect(&mut c).await;
    let names: BTreeSet<String> = second
        .iter()
        .filter(|s| s.metric == "sermon_docker_processes")
        .map(|s| s.labels[1].1.clone())
        .collect();
    assert_eq!(names, ["httpd".to_owned(), "sh".to_owned()].into());
    let int = |m, id| value(&second, m, id);
    assert_eq!(
        int("sermon_docker_cpu_utilization_ratio", WEB),
        Some(V::Int(28_259))
    );
    assert_eq!(
        int("sermon_docker_cpu_utilization_ratio", CLIENT),
        Some(V::Int(45_229))
    );
    assert_eq!(
        int("sermon_docker_memory_used_bytes", WEB),
        Some(V::Int(3_686_400))
    );
    assert_eq!(
        int("sermon_docker_memory_limit_bytes", WEB),
        None,
        "no limit"
    );
    assert_eq!(
        int("sermon_docker_memory_limit_bytes", DISK),
        Some(V::Int(268_435_456))
    );
    assert_eq!(
        int("sermon_docker_write_bytes_per_second", DISK),
        Some(V::Int(1_719_664_640))
    );
    assert_eq!(
        int("sermon_docker_receive_bytes_per_second", WEB),
        Some(V::Int(2_387_764))
    );
    assert_eq!(
        int("sermon_docker_transmit_bytes_per_second", WEB),
        Some(V::Int(385_425_011))
    );
    assert_eq!(int("sermon_docker_oom_kills", DISK), Some(V::Int(0)));
    assert_eq!(int("sermon_docker_processes", DISK), Some(V::Int(2)));
    assert!(second.len() <= 2 + 3 * PER_CONTAINER);
    crate::assert_in_catalog("docker", &first);
    crate::assert_in_catalog("docker", &second);
}

// Podman (cgroupfs manager, as captured): its containers share the host's network, so they
// report no traffic of their own.
#[tokio::test(start_paused = true)]
async fn reads_podmans_containers() {
    let (t0, t1) = (tree("podman/t0", &[]), tree("podman/t1", &[]));
    let mut c = Containers::new(ProcRoot::new(t0.path()), 20);
    collect(&mut c).await;
    tokio::time::advance(Duration::from_secs(5)).await;
    c.root = ProcRoot::new(t1.path());
    let s = collect(&mut c).await;
    assert_eq!(total(&s, "sermon_docker_containers"), V::Int(2));
    assert_eq!(
        value(&s, "sermon_docker_cpu_utilization_ratio", BUSY),
        Some(V::Int(62_829)),
        "one core of 16"
    );
    assert_eq!(
        value(&s, "sermon_docker_cpu_utilization_ratio", IDLE),
        Some(V::Int(0))
    );
    assert_eq!(
        value(&s, "sermon_docker_receive_bytes_per_second", BUSY),
        None,
        "host network"
    );
    let busy = s
        .iter()
        .find(|s| s.metric == "sermon_docker_processes" && s.labels[0].1 == BUSY[..12])
        .unwrap();
    assert_eq!(
        busy.labels[1..],
        [("name", "sh".to_owned()), ("runtime", "podman".to_owned())]
    );
}

// The systemd drivers: system.slice/docker-<id>.scope, machine.slice/libpod-<id>.scope (the
// processes in its child `container`, conmon beside it), rootless below the user's manager.
#[test]
fn finds_the_containers_of_every_driver() {
    let docker = tree(
        "docker/t1",
        &[
            (
                &format!("docker/{WEB}"),
                &format!("system.slice/docker-{WEB}.scope"),
            ),
            (
                &format!("docker/{CLIENT}"),
                &format!(
                    "user.slice/user-1000.slice/user@1000.service/user.slice/docker-{CLIENT}.scope"
                ),
            ),
        ],
    );
    let found = scan(&ProcRoot::new(docker.path())).unwrap();
    let dirs: Vec<&str> = found.iter().map(|f| f.dir.as_str()).collect();
    assert_eq!(dirs.len(), 3, "{dirs:?}");
    assert!(dirs.contains(&format!("system.slice/docker-{WEB}.scope").as_str()));

    let podman = tree(
        "podman/t1",
        &[(
            &format!("libpod_parent/libpod-{BUSY}"),
            &format!("machine.slice/libpod-{BUSY}.scope"),
        )],
    );
    let scope = podman
        .path()
        .join(format!("sys/fs/cgroup/machine.slice/libpod-{BUSY}.scope"));
    std::fs::create_dir(scope.join("container")).unwrap();
    std::fs::rename(
        scope.join("cgroup.procs"),
        scope.join("container/cgroup.procs"),
    )
    .unwrap();
    std::fs::write(scope.join("cgroup.procs"), "").unwrap();
    std::fs::create_dir_all(podman.path().join(format!(
        "sys/fs/cgroup/machine.slice/libpod-conmon-{BUSY}.scope"
    )))
    .unwrap();
    let root = ProcRoot::new(podman.path());
    let found = scan(&root).unwrap();
    assert_eq!(found.len(), 2, "conmon is no container: {found:?}");
    let busy = found.iter().find(|f| f.id == BUSY).unwrap();
    assert_eq!(busy.runtime, Runtime::Podman);
    assert_eq!(cgroups::read(&root, &busy.dir, &BTreeSet::new()).name, "sh");
}

// Containers come and go: a new one has no rate in its first run, a gone one leaves nothing.
#[tokio::test(start_paused = true)]
async fn containers_come_and_go() {
    let t0 = tree("docker/t0", &[]);
    let t1 = tree("docker/t1", &[]);
    let cg = t1.path().join("sys/fs/cgroup/docker");
    std::fs::remove_dir_all(cg.join(CLIENT)).unwrap();
    let new = "d0c9000000000000000000000000000000000000000000000000000000000000";
    copy(&cg.join(WEB), &cg.join(new));
    let mut c = Containers::new(ProcRoot::new(t0.path()), 20);
    collect(&mut c).await;
    tokio::time::advance(Duration::from_secs(5)).await;
    c.root = ProcRoot::new(t1.path());
    let s = collect(&mut c).await;
    assert_eq!(total(&s, "sermon_docker_containers"), V::Int(3));
    assert!(
        value(&s, "sermon_docker_processes", CLIENT).is_none(),
        "gone"
    );
    assert!(value(&s, "sermon_docker_processes", new).is_some(), "new");
    assert!(
        value(&s, "sermon_docker_cpu_utilization_ratio", new).is_none(),
        "no rate in its first run"
    );
    assert!(value(&s, "sermon_docker_cpu_utilization_ratio", WEB).is_some());
    assert_eq!(c.last.len(), 3);
    assert!(!c.last.contains_key(CLIENT));
}

// At most max_containers, the largest by memory; without cgroup v2 a reason.
#[tokio::test(start_paused = true)]
async fn reports_within_bounds() {
    let t1 = tree("docker/t1", &[]);
    let mut all = Containers::new(ProcRoot::new(t1.path()), 3);
    assert_eq!(
        total(&collect(&mut all).await, "sermon_docker_truncated"),
        V::Int(0),
        "all of them"
    );
    assert_eq!(
        Containers::new(ProcRoot::new(t1.path()), 1000).max,
        MAX_CONTAINERS
    );
    assert_eq!(Containers::new(ProcRoot::new(t1.path()), 0).max, 1);
    let mut c = Containers::new(ProcRoot::new(t1.path()), 1);
    let s = collect(&mut c).await;
    assert_eq!(total(&s, "sermon_docker_truncated"), V::Int(1));
    assert!(
        value(&s, "sermon_docker_processes", CLIENT).is_some(),
        "the largest"
    );
    assert!(value(&s, "sermon_docker_processes", WEB).is_none());

    // Many containers: at most 512 read, the samples within the runtime's bound.
    let many = tempfile::tempdir().unwrap();
    let cg = many.path().join("sys/fs/cgroup");
    std::fs::create_dir_all(&cg).unwrap();
    std::fs::write(cg.join("cgroup.controllers"), "cpu memory io pids").unwrap();
    for i in 0..600 {
        let dir = cg.join(format!("docker/{i:064x}"));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("memory.current"), i.to_string()).unwrap();
        std::fs::write(dir.join("cpu.stat"), "usage_usec 5\n").unwrap();
    }
    let root = ProcRoot::new(many.path());
    assert_eq!(scan(&root).unwrap().len(), cgroups::MAX_SCANNED);
    let mut c = Containers::new(root, MAX_CONTAINERS);
    collect(&mut c).await;
    tokio::time::advance(Duration::from_secs(5)).await;
    let s = collect(&mut c).await;
    assert!(s.len() <= MAX_SAMPLES, "{}", s.len());

    let v1 = tempfile::tempdir().unwrap();
    let mut c = Containers::new(ProcRoot::new(v1.path()), 20);
    let Err(RunError::Failed(why)) = c.collect(&Run::default()).await else {
        panic!("cgroup v1");
    };
    assert!(why.contains("cgroup v2"), "{why}");
}

// Foreign files: nothing panics, odd values are left out.
#[test]
fn takes_only_what_holds() {
    assert_eq!(
        cgroups::field("usage_usec 12\nuser_usec 3\n", "usage_usec"),
        Some(12)
    );
    assert_eq!(cgroups::field("usage_usec_x 12\n", "usage_usec"), None);
    assert_eq!(cgroups::field("usage_usec -1\n", "usage_usec"), None);
    assert_eq!(
        cgroups::io("8:0 rbytes=10 wbytes=20 rios=1\n259:0 rbytes=5 wbytes=x\nbroken\n"),
        (15, 20)
    );
    assert_eq!(cgroups::name("evil\u{1b}[2J\n"), "evil[2J");
    assert!(cgroups::name(&"ä".repeat(100)).len() <= 64);
    let dir = tree("docker/t1", &[]);
    let web = dir.path().join(format!("sys/fs/cgroup/docker/{WEB}"));
    for (file, text) in [
        ("memory.max", "max\n"),
        ("memory.current", "nan"),
        ("cgroup.procs", "0\n"),
    ] {
        std::fs::write(web.join(file), text).unwrap();
    }
    // PID 0: a process of another PID namespace, not /proc/0.
    std::fs::create_dir_all(dir.path().join("proc/0/net")).unwrap();
    std::fs::write(dir.path().join("proc/0/comm"), "other\n").unwrap();
    let read = || {
        cgroups::read(
            &ProcRoot::new(dir.path()),
            &format!("docker/{WEB}"),
            &BTreeSet::new(),
        )
    };
    let r = read();
    assert_eq!(
        (r.limit, r.memory, r.name.as_str(), r.net),
        (None, None, "", None)
    );
    // The traffic without lo.
    std::fs::write(web.join("cgroup.procs"), "7\n").unwrap();
    std::fs::create_dir_all(dir.path().join("proc/7/net")).unwrap();
    std::fs::write(dir.path().join("proc/7/comm"), "app\n").unwrap();
    std::fs::write(
        dir.path().join("proc/7/net/dev"),
        "Inter-|   Receive |  Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n    lo: 1000 1 0 0 0 0 0 0 1000 1 0 0 0 0 0 0\n  eth0: 5 1 0 0 0 0 0 0 7 1 0 0 0 0 0 0\n",
    )
    .unwrap();
    let r = read();
    assert_eq!((r.name.as_str(), r.net), ("app", Some((5, 7))));
}

#[test]
fn dockerd_or_conmon_suggest_the_module() {
    assert!(Docker.discover(&HostFacts::default()).is_none());
    let facts = HostFacts {
        processes: ["conmon".to_owned()].into(),
        ..HostFacts::default()
    };
    assert!(Docker.discover(&facts).is_some());
}
