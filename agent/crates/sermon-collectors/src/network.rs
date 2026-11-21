//! Network: `/proc/net/{dev,snmp,netstat,sockstat}`, `/sys/class/net` and conntrack
//! (KONZEPT §8.1).
//!
//! | Metric | Labels | Unit |
//! |---|---|---|
//! | `sermon_network_{receive,transmit}_bytes_per_second` | `interface` | bytes/s |
//! | `sermon_network_{receive,transmit}_packets_per_second` | `interface` | packets/s |
//! | `sermon_network_{receive,transmit}_{errors,drops}_per_second` | `interface` | 1/s |
//! | `sermon_network_up` | `interface` | 1 = operstate up |
//! | `sermon_network_speed_bits_per_second` | `interface` | bits/s (if the driver reports it) |
//! | `sermon_tcp_established`, `sermon_tcp_time_wait` | – | connections |
//! | `sermon_tcp_{retransmits,listen_overflows,listen_drops}_per_second` | – | 1/s |
//! | `sermon_conntrack_entries`, `sermon_conntrack_limit`, `sermon_conntrack_utilization_ratio` | – | count, ppm |
//!
//! Left out: `lo`, interfaces of containers and VMs (`veth*`, `docker*`, `br-*`, `virbr*`,
//! `vnet*`, `cali*`, `flannel*`, `cni*`) and interfaces that never carried a packet (unused
//! tunnel devices). At most 64 interfaces.

use std::collections::{BTreeMap, HashMap};
use std::time::Duration;

use sermon_core::{Monotonic, Sample};

use crate::rate::{per_second, per_second_float, ppm};
use crate::{CollectError, Collector, Context, optional};

const IGNORED_PREFIXES: [&str; 8] = [
    "veth", "docker", "br-", "virbr", "vnet", "cali", "flannel", "cni",
];
const MAX_INTERFACES: usize = 64;

/// Counters of one interface from `/proc/net/dev`.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct InterfaceCounters {
    /// Received bytes, packets, errors, drops.
    pub receive: [u64; 4],
    /// Transmitted bytes, packets, errors, drops.
    pub transmit: [u64; 4],
}

/// Parses `/proc/net/dev`.
#[must_use]
pub fn parse_net_dev(text: &str) -> BTreeMap<String, InterfaceCounters> {
    let mut interfaces = BTreeMap::new();
    for line in text.lines() {
        let Some((name, values)) = line.split_once(':') else {
            continue;
        };
        let values: Vec<u64> = values
            .split_whitespace()
            .filter_map(|value| value.parse().ok())
            .collect();
        if values.len() < 16 {
            continue;
        }
        interfaces.insert(
            name.trim().to_owned(),
            InterfaceCounters {
                receive: [values[0], values[1], values[2], values[3]],
                transmit: [values[8], values[9], values[10], values[11]],
            },
        );
    }
    interfaces
}

/// Parses the header/value line pairs of `/proc/net/snmp` and `/proc/net/netstat` into
/// `"Tcp.CurrEstab" → value`.
#[must_use]
pub fn parse_snmp_table(text: &str) -> HashMap<String, i64> {
    let mut values = HashMap::new();
    let lines: Vec<&str> = text.lines().collect();
    let mut index = 0;
    while index + 1 < lines.len() {
        let header = lines[index].split_once(':');
        let numbers = lines[index + 1].split_once(':');
        match (header, numbers) {
            (Some((prefix, names)), Some((value_prefix, numbers)))
                if prefix == value_prefix
                    && numbers.split_whitespace().all(|n| n.parse::<i64>().is_ok()) =>
            {
                for (name, number) in names.split_whitespace().zip(numbers.split_whitespace()) {
                    if let Ok(number) = number.parse::<i64>() {
                        values.insert(format!("{prefix}.{name}"), number);
                    }
                }
                index += 2;
            }
            _ => index += 1,
        }
    }
    values
}

/// Sockets in `TIME_WAIT` from `/proc/net/sockstat`.
#[must_use]
pub fn parse_time_wait(text: &str) -> Option<i64> {
    let line = text.lines().find_map(|line| line.strip_prefix("TCP:"))?;
    let fields: Vec<&str> = line.split_whitespace().collect();
    fields
        .windows(2)
        .find(|pair| pair[0] == "tw")
        .and_then(|pair| pair[1].parse().ok())
}

fn reported(name: &str, counters: &InterfaceCounters) -> bool {
    name != "lo"
        && !IGNORED_PREFIXES
            .iter()
            .any(|prefix| name.starts_with(prefix))
        && counters.receive[1].saturating_add(counters.transmit[1]) > 0
}

/// Rates of one interface from two snapshots.
#[must_use]
pub fn interface_samples(
    interface: &str,
    previous: &InterfaceCounters,
    current: &InterfaceCounters,
    elapsed: Duration,
) -> Vec<Sample> {
    const NAMES: [[&str; 4]; 2] = [
        [
            "sermon_network_receive_bytes_per_second",
            "sermon_network_receive_packets_per_second",
            "sermon_network_receive_errors_per_second",
            "sermon_network_receive_drops_per_second",
        ],
        [
            "sermon_network_transmit_bytes_per_second",
            "sermon_network_transmit_packets_per_second",
            "sermon_network_transmit_errors_per_second",
            "sermon_network_transmit_drops_per_second",
        ],
    ];
    let mut samples = Vec::new();
    for (names, (before, now)) in NAMES.iter().zip([
        (previous.receive, current.receive),
        (previous.transmit, current.transmit),
    ]) {
        for index in 0..4 {
            let sample = if index < 2 {
                per_second(before[index], now[index], elapsed)
                    .map(|rate| Sample::int(names[index], rate))
            } else {
                per_second_float(before[index], now[index], elapsed)
                    .map(|rate| Sample::float(names[index], rate))
            };
            samples.extend(sample.map(|sample| sample.label("interface", interface)));
        }
    }
    samples
}

#[derive(Debug, Default)]
struct Snapshot {
    interfaces: BTreeMap<String, InterfaceCounters>,
    tcp: HashMap<String, i64>,
}

/// Network collector.
#[derive(Debug, Default)]
pub struct NetworkCollector {
    previous: Option<(Monotonic, Snapshot)>,
}

impl NetworkCollector {
    fn link_samples(context: &Context<'_>, interface: &str, out: &mut Vec<Sample>) {
        let read = |attribute: &str| {
            context
                .root
                .read_sys(&format!("class/net/{interface}/{attribute}"))
                .ok()
        };
        if let Some(state) = read("operstate") {
            out.push(
                Sample::int("sermon_network_up", i64::from(state.trim() == "up"))
                    .label("interface", interface),
            );
        }
        // -1 or an error (EINVAL) for links without a known speed.
        if let Some(megabits) = read("speed")
            .and_then(|speed| speed.trim().parse::<i64>().ok())
            .filter(|speed| *speed > 0)
        {
            out.push(
                Sample::int(
                    "sermon_network_speed_bits_per_second",
                    megabits.saturating_mul(1_000_000),
                )
                .label("interface", interface),
            );
        }
    }
}

impl Collector for NetworkCollector {
    fn name(&self) -> &'static str {
        "network"
    }

    fn collect(
        &mut self,
        context: &Context<'_>,
        out: &mut Vec<Sample>,
    ) -> Result<(), CollectError> {
        let mut interfaces = parse_net_dev(&context.root.read_proc("net/dev")?);
        interfaces.retain(|name, counters| reported(name, counters));
        while interfaces.len() > MAX_INTERFACES {
            interfaces.pop_last();
        }
        let mut tcp = HashMap::new();
        for file in ["net/snmp", "net/netstat"] {
            if let Some(text) = optional(context.root.read_proc(file))? {
                tcp.extend(parse_snmp_table(&text));
            }
        }

        for interface in interfaces.keys() {
            Self::link_samples(context, interface, out);
        }
        if let Some(&established) = tcp.get("Tcp.CurrEstab") {
            out.push(Sample::int("sermon_tcp_established", established));
        }
        if let Some(time_wait) = optional(context.root.read_proc("net/sockstat"))?
            .as_deref()
            .and_then(parse_time_wait)
        {
            out.push(Sample::int("sermon_tcp_time_wait", time_wait));
        }
        conntrack_samples(context, out)?;

        if let Some((at, previous)) = &self.previous {
            let elapsed = context.now.saturating_duration_since(*at);
            for (name, current) in &interfaces {
                if let Some(before) = previous.interfaces.get(name) {
                    out.extend(interface_samples(name, before, current, elapsed));
                }
            }
            let counters = [
                ("sermon_tcp_retransmits_per_second", "Tcp.RetransSegs"),
                (
                    "sermon_tcp_listen_overflows_per_second",
                    "TcpExt.ListenOverflows",
                ),
                ("sermon_tcp_listen_drops_per_second", "TcpExt.ListenDrops"),
            ];
            for (metric, key) in counters {
                let pair = previous.tcp.get(key).zip(tcp.get(key));
                let rate = pair.and_then(|(before, now)| {
                    per_second_float(
                        u64::try_from(*before).ok()?,
                        u64::try_from(*now).ok()?,
                        elapsed,
                    )
                });
                if let Some(rate) = rate {
                    out.push(Sample::float(metric, rate));
                }
            }
        }
        self.previous = Some((context.now, Snapshot { interfaces, tcp }));
        Ok(())
    }
}

fn conntrack_samples(context: &Context<'_>, out: &mut Vec<Sample>) -> Result<(), CollectError> {
    let read = |file| -> Result<Option<i64>, CollectError> {
        Ok(optional(context.root.read_proc(file))?.and_then(|text| text.trim().parse().ok()))
    };
    let entries = read("sys/net/netfilter/nf_conntrack_count")?;
    let limit = read("sys/net/netfilter/nf_conntrack_max")?;
    if let Some(entries) = entries {
        out.push(Sample::int("sermon_conntrack_entries", entries));
    }
    if let Some(limit) = limit {
        out.push(Sample::int("sermon_conntrack_limit", limit));
    }
    let ratio = entries
        .zip(limit)
        .and_then(|(entries, limit)| ppm(u64::try_from(entries).ok()?, u64::try_from(limit).ok()?));
    if let Some(ratio) = ratio {
        out.push(Sample::int("sermon_conntrack_utilization_ratio", ratio));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use proptest::prelude::*;

    use super::*;
    use crate::testing::{at, fixture, float, int, root_with};

    #[test]
    fn parses_the_fixtures() {
        for distro in [
            "debian-13",
            "ubuntu-24.04",
            "rockylinux-9",
            "almalinux-10",
            "fedora-44",
        ] {
            let root = fixture(distro);
            let interfaces = parse_net_dev(&root.read_proc("net/dev").unwrap());
            assert!(
                interfaces.contains_key("eth0") && interfaces.contains_key("lo"),
                "{distro}"
            );
            let tcp = parse_snmp_table(&root.read_proc("net/snmp").unwrap());
            assert!(
                tcp.contains_key("Tcp.CurrEstab") && tcp.contains_key("Tcp.RetransSegs"),
                "{distro}"
            );
            let ext = parse_snmp_table(&root.read_proc("net/netstat").unwrap());
            assert!(ext.contains_key("TcpExt.ListenOverflows"), "{distro}");
            assert_eq!(
                parse_time_wait(&root.read_proc("net/sockstat").unwrap()),
                Some(0),
                "{distro}"
            );
        }
    }

    #[test]
    fn reports_only_real_interfaces_with_link_state_and_tcp_figures() {
        let root = fixture("debian-13");
        let mut collector = NetworkCollector::default();
        let mut first = Vec::new();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut first,
            )
            .unwrap();
        let mut second = Vec::new();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(1),
                },
                &mut second,
            )
            .unwrap();

        let interfaces: std::collections::BTreeSet<&str> = second
            .iter()
            .filter_map(|sample| sample.labels.iter().find(|(name, _)| *name == "interface"))
            .map(|(_, interface)| interface.as_str())
            .collect();
        assert_eq!(
            interfaces,
            std::collections::BTreeSet::from(["eth0"]),
            "lo and unused tunnels left out"
        );
        assert_eq!(
            int(&second, "sermon_network_up", &[("interface", "eth0")]),
            Some(1)
        );
        assert_eq!(
            int(
                &second,
                "sermon_network_speed_bits_per_second",
                &[("interface", "eth0")]
            ),
            Some(10_000_000_000)
        );
        assert_eq!(
            int(
                &second,
                "sermon_network_receive_bytes_per_second",
                &[("interface", "eth0")]
            ),
            Some(0)
        );
        assert!(int(&second, "sermon_tcp_established", &[]).is_some());
        assert_eq!(
            float(&second, "sermon_tcp_retransmits_per_second", &[]),
            Some(0.0)
        );
        assert!(int(&second, "sermon_conntrack_limit", &[]).is_some());
    }

    #[test]
    fn rates_per_interface() {
        let dev = |rx: u64, tx: u64| {
            format!(
                "Inter-|   Receive\n face |bytes\n  eth0: {rx} 10 1 2 0 0 0 0 {tx} 20 0 0 0 0 0 0\n veth12ab: 5 5 0 0 0 0 0 0 5 5 0 0 0 0 0 0\n"
            )
        };
        let (_dir, root) = root_with(&[("proc/net/dev", &dev(1000, 2000))]);
        let mut collector = NetworkCollector::default();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut Vec::new(),
            )
            .unwrap();
        std::fs::write(root.proc("net/dev").unwrap(), dev(3000, 2500)).unwrap();

        let mut samples = Vec::new();
        collector
            .collect(
                &Context {
                    root: &root,
                    now: at(2),
                },
                &mut samples,
            )
            .unwrap();

        let eth0 = [("interface", "eth0")];
        assert_eq!(
            int(&samples, "sermon_network_receive_bytes_per_second", &eth0),
            Some(1000)
        );
        assert_eq!(
            int(&samples, "sermon_network_transmit_bytes_per_second", &eth0),
            Some(250)
        );
        assert_eq!(
            float(&samples, "sermon_network_receive_errors_per_second", &eth0),
            Some(0.0)
        );
        assert!(
            samples
                .iter()
                .all(|sample| !sample.labels.iter().any(|(_, v)| v.starts_with("veth")))
        );
        assert_eq!(
            int(&samples, "sermon_tcp_established", &[]),
            None,
            "no snmp file: no TCP figures"
        );
    }

    #[test]
    fn conntrack_utilization() {
        let (_dir, root) = root_with(&[
            ("proc/net/dev", ""),
            ("proc/sys/net/netfilter/nf_conntrack_count", "6553\n"),
            ("proc/sys/net/netfilter/nf_conntrack_max", "65536\n"),
        ]);
        let mut samples = Vec::new();
        NetworkCollector::default()
            .collect(
                &Context {
                    root: &root,
                    now: at(0),
                },
                &mut samples,
            )
            .unwrap();

        assert_eq!(
            int(&samples, "sermon_conntrack_utilization_ratio", &[]),
            Some(99_991)
        );
    }

    proptest! {
        #[test]
        fn parsers_never_panic(text in "\\PC*") {
            let _ = parse_net_dev(&text);
            let _ = parse_snmp_table(&text);
            let _ = parse_time_wait(&text);
        }
    }
}
