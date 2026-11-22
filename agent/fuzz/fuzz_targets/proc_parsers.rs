//! Every parser of `/proc` and `/sys` text (and of `systemctl` output), and the rates computed from two readings, must handle
//! any input without panicking (overflow checks are on in fuzz builds).
//!
//! Input: one byte for the elapsed time, then two readings separated by a NUL byte.
#![no_main]

use std::time::Duration;

use libfuzzer_sys::fuzz_target;
use sermon_collectors::{
    agent, cpu, disk, filesystem, host, memory, network, pressure, raid, system, systemd,
};

fn parse_all(text: &str) {
    let _ = host::parse_os_release(text);
    let _ = host::parse_mem_total(text);
    let _ = host::count_cpus(text);
    let _ = cpu::parse_loadavg(text);
    let _ = pressure::parse_pressure(text);
    let _ = memory::parse_vmstat(text);
    if let Ok(info) = memory::parse_meminfo(text) {
        let _ = memory::memory_samples(&info);
    }
    let _ = filesystem::reported_mounts(filesystem::parse_mountinfo(text));
    let _ = system::parse_uptime(text);
    let _ = system::parse_threads(text);
    let _ = system::parse_file_nr(text);
    let _ = system::is_zombie(text);
    let _ = agent::parse_cpu_ticks(text);
    let _ = agent::parse_rss(text);
    let _ = network::parse_snmp_table(text);
    let _ = network::parse_time_wait(text);
    // M2-22: /proc/mdstat and the output of systemctl.
    let _ = raid::parse_mdstat(text);
    let _ = systemd::parse_failed(text);
}

fuzz_target!(|data: &[u8]| {
    let Some((&elapsed, rest)) = data.split_first() else {
        return;
    };
    let elapsed = Duration::from_millis(u64::from(elapsed) * 50);
    let (first, second) = match rest.iter().position(|&b| b == 0) {
        Some(i) => (&rest[..i], &rest[i + 1..]),
        None => (rest, rest),
    };
    let (Ok(previous), Ok(current)) = (std::str::from_utf8(first), std::str::from_utf8(second))
    else {
        return;
    };
    parse_all(previous);
    parse_all(current);

    if let (Ok(a), Ok(b)) = (cpu::parse_stat(previous), cpu::parse_stat(current)) {
        let _ = cpu::utilization(&a, &b, elapsed);
    }
    let (a, b) = (
        disk::parse_diskstats(previous),
        disk::parse_diskstats(current),
    );
    for (device, now) in &b {
        if let Some(before) = a.get(device) {
            let _ = disk::device_samples(device, before, now, elapsed);
        }
    }
    let (a, b) = (
        network::parse_net_dev(previous),
        network::parse_net_dev(current),
    );
    for (interface, now) in &b {
        if let Some(before) = a.get(interface) {
            let _ = network::interface_samples(interface, before, now, elapsed);
        }
    }
});
