// Package metric is the catalog of metrics SerMon knows (KONZEPT §8, M1-25): name, unit, labels
// and how charts aggregate them. It is the source for the UI (units, display names, help) and for
// the validation at ingest: unknown metrics and unexpected labels are dropped.
package metric

import (
	"slices"
	"strings"
)

// Unit tells the UI how to scale and format values.
type Unit string

// Units. Ratios are integers in ppm (1 000 000 = 100 %).
const (
	UnitRatioPPM       Unit = "ratio_ppm"
	UnitBytes          Unit = "bytes"
	UnitBytesPerSecond Unit = "bytes_per_second"
	UnitBitsPerSecond  Unit = "bits_per_second"
	UnitPerSecond      Unit = "per_second"
	UnitPagesPerSecond Unit = "pages_per_second"
	UnitCount          Unit = "count"
	UnitSeconds        Unit = "seconds"
	UnitTimestamp      Unit = "timestamp_seconds"
	UnitCelsius        Unit = "celsius"
	UnitLoad           Unit = "load"
	UnitBoolean        Unit = "boolean"
)

// Aggregation is the value charts show for downsampled data (the aggregates store all of them).
type Aggregation string

// Aggregations.
const (
	Avg  Aggregation = "avg"
	Max  Aggregation = "max"
	Last Aggregation = "last"
)

// Area groups metrics for the detail views.
type Area string

// Areas.
const (
	AreaCPU        Area = "cpu"
	AreaMemory     Area = "memory"
	AreaDisk       Area = "disk"
	AreaFilesystem Area = "filesystem"
	AreaNetwork    Area = "network"
	AreaSystem     Area = "system"
	AreaAgent      Area = "agent"
)

// Definition describes one metric.
type Definition struct {
	Name        string
	Unit        Unit
	Labels      []string // exactly these labels, sorted
	Aggregation Aggregation
	Area        Area
	// Server: the server derives the metric itself (M4-16); samples of an agent are dropped.
	Server bool
}

// DisplayKey is the i18n message key of the metric's name.
func (d Definition) DisplayKey() string { return "metric_" + strings.TrimPrefix(d.Name, "sermon_") }

// HelpKey is the help text of the metric (docs/help/{de,en}/metrics/<name>.md).
func (d Definition) HelpKey() string { return "metrics/" + d.Name }

// AcceptsLabels reports whether a sample has exactly the labels of the metric.
func (d Definition) AcceptsLabels(labels map[string]string) bool {
	if len(labels) != len(d.Labels) {
		return false
	}
	for _, name := range d.Labels {
		if _, ok := labels[name]; !ok {
			return false
		}
	}
	return true
}

func def(name string, unit Unit, aggregation Aggregation, area Area, labels ...string) Definition {
	slices.Sort(labels)
	return Definition{Name: name, Unit: unit, Labels: labels, Aggregation: aggregation, Area: area}
}

// definitions is the catalog; keep in sync with the agent collectors (agent/crates/sermon-collectors)
// – the end-to-end test fails when the agent sends a metric that is missing here.
var definitions = []Definition{
	// CPU and load
	def("sermon_cpu_utilization_ratio", UnitRatioPPM, Avg, AreaCPU, "mode"),
	def("sermon_cpu_core_busy_ratio", UnitRatioPPM, Avg, AreaCPU, "cpu"),
	def("sermon_cpu_context_switches_per_second", UnitPerSecond, Avg, AreaCPU),
	def("sermon_cpu_interrupts_per_second", UnitPerSecond, Avg, AreaCPU),
	def("sermon_cpu_forks_per_second", UnitPerSecond, Avg, AreaCPU),
	def("sermon_cpu_logical_count", UnitCount, Last, AreaCPU),
	def("sermon_processes_running", UnitCount, Avg, AreaCPU),
	def("sermon_processes_blocked", UnitCount, Avg, AreaCPU),
	def("sermon_load_average", UnitLoad, Avg, AreaCPU, "period"),
	def("sermon_load_average_per_cpu", UnitLoad, Avg, AreaCPU, "period"),
	def("sermon_pressure_stall_ratio", UnitRatioPPM, Avg, AreaCPU, "resource", "kind"),

	// Memory and swap
	def("sermon_memory_total_bytes", UnitBytes, Last, AreaMemory),
	def("sermon_memory_free_bytes", UnitBytes, Avg, AreaMemory),
	def("sermon_memory_available_bytes", UnitBytes, Avg, AreaMemory),
	def("sermon_memory_used_bytes", UnitBytes, Avg, AreaMemory),
	def("sermon_memory_buffers_bytes", UnitBytes, Avg, AreaMemory),
	def("sermon_memory_cached_bytes", UnitBytes, Avg, AreaMemory),
	def("sermon_memory_shared_bytes", UnitBytes, Avg, AreaMemory),
	def("sermon_memory_slab_bytes", UnitBytes, Avg, AreaMemory),
	def("sermon_memory_utilization_ratio", UnitRatioPPM, Avg, AreaMemory),
	def("sermon_swap_total_bytes", UnitBytes, Last, AreaMemory),
	def("sermon_swap_used_bytes", UnitBytes, Avg, AreaMemory),
	def("sermon_swap_utilization_ratio", UnitRatioPPM, Avg, AreaMemory),
	def("sermon_swap_in_pages_per_second", UnitPagesPerSecond, Avg, AreaMemory),
	def("sermon_swap_out_pages_per_second", UnitPagesPerSecond, Avg, AreaMemory),
	def("sermon_memory_major_faults_per_second", UnitPerSecond, Avg, AreaMemory),
	def("sermon_memory_oom_kills_per_second", UnitPerSecond, Max, AreaMemory),

	// Disk I/O
	def("sermon_disk_read_bytes_per_second", UnitBytesPerSecond, Avg, AreaDisk, "device"),
	def("sermon_disk_write_bytes_per_second", UnitBytesPerSecond, Avg, AreaDisk, "device"),
	def("sermon_disk_reads_per_second", UnitPerSecond, Avg, AreaDisk, "device"),
	def("sermon_disk_writes_per_second", UnitPerSecond, Avg, AreaDisk, "device"),
	def("sermon_disk_busy_ratio", UnitRatioPPM, Avg, AreaDisk, "device"),
	def("sermon_disk_await_seconds", UnitSeconds, Avg, AreaDisk, "device"),
	def("sermon_disk_queue_length", UnitCount, Avg, AreaDisk, "device"),

	// File systems
	def("sermon_filesystem_size_bytes", UnitBytes, Last, AreaFilesystem, "mountpoint"),
	def("sermon_filesystem_avail_bytes", UnitBytes, Last, AreaFilesystem, "mountpoint"),
	def("sermon_filesystem_used_bytes", UnitBytes, Last, AreaFilesystem, "mountpoint"),
	def("sermon_filesystem_utilization_ratio", UnitRatioPPM, Max, AreaFilesystem, "mountpoint"),
	def("sermon_filesystem_inodes_total", UnitCount, Last, AreaFilesystem, "mountpoint"),
	def("sermon_filesystem_inodes_used", UnitCount, Last, AreaFilesystem, "mountpoint"),
	def("sermon_filesystem_readonly", UnitBoolean, Max, AreaFilesystem, "mountpoint"),

	// Software RAID (M2-22)
	def("sermon_raid_degraded", UnitBoolean, Max, AreaDisk, "device"),
	def("sermon_raid_disks_missing", UnitCount, Max, AreaDisk, "device"),
	def("sermon_raid_sync_ratio", UnitRatioPPM, Last, AreaDisk, "device"),

	// Network
	def("sermon_network_receive_bytes_per_second", UnitBytesPerSecond, Avg, AreaNetwork, "interface"),
	def("sermon_network_transmit_bytes_per_second", UnitBytesPerSecond, Avg, AreaNetwork, "interface"),
	def("sermon_network_receive_packets_per_second", UnitPerSecond, Avg, AreaNetwork, "interface"),
	def("sermon_network_transmit_packets_per_second", UnitPerSecond, Avg, AreaNetwork, "interface"),
	def("sermon_network_receive_errors_per_second", UnitPerSecond, Max, AreaNetwork, "interface"),
	def("sermon_network_transmit_errors_per_second", UnitPerSecond, Max, AreaNetwork, "interface"),
	def("sermon_network_receive_drops_per_second", UnitPerSecond, Max, AreaNetwork, "interface"),
	def("sermon_network_transmit_drops_per_second", UnitPerSecond, Max, AreaNetwork, "interface"),
	def("sermon_network_up", UnitBoolean, Last, AreaNetwork, "interface"),
	def("sermon_network_speed_bits_per_second", UnitBitsPerSecond, Last, AreaNetwork, "interface"),
	def("sermon_tcp_established", UnitCount, Avg, AreaNetwork),
	def("sermon_tcp_time_wait", UnitCount, Avg, AreaNetwork),
	def("sermon_tcp_retransmits_per_second", UnitPerSecond, Avg, AreaNetwork),
	def("sermon_tcp_listen_overflows_per_second", UnitPerSecond, Max, AreaNetwork),
	def("sermon_tcp_listen_drops_per_second", UnitPerSecond, Max, AreaNetwork),
	def("sermon_conntrack_entries", UnitCount, Avg, AreaNetwork),
	def("sermon_conntrack_limit", UnitCount, Last, AreaNetwork),
	def("sermon_conntrack_utilization_ratio", UnitRatioPPM, Max, AreaNetwork),

	// System
	def("sermon_system_uptime_seconds", UnitSeconds, Last, AreaSystem),
	def("sermon_system_boot_time_seconds", UnitTimestamp, Last, AreaSystem),
	def("sermon_system_processes", UnitCount, Avg, AreaSystem),
	def("sermon_system_threads", UnitCount, Avg, AreaSystem),
	def("sermon_system_zombies", UnitCount, Max, AreaSystem),
	def("sermon_system_file_descriptors_used", UnitCount, Avg, AreaSystem),
	def("sermon_system_file_descriptors_limit", UnitCount, Last, AreaSystem),
	def("sermon_system_entropy_bits", UnitCount, Avg, AreaSystem),
	def("sermon_hardware_temperature_celsius", UnitCelsius, Max, AreaSystem, "sensor"),
	// Time synchronisation and failed systemd units (M2-22)
	def("sermon_time_synchronized", UnitBoolean, Last, AreaSystem),
	def("sermon_time_offset_seconds", UnitSeconds, Avg, AreaSystem),
	def("sermon_time_max_error_seconds", UnitSeconds, Max, AreaSystem),
	def("sermon_systemd_unit_failed", UnitBoolean, Max, AreaSystem, "unit"),

	// The agent itself
	def("sermon_agent_cpu_ratio", UnitRatioPPM, Avg, AreaAgent),
	def("sermon_agent_memory_rss_bytes", UnitBytes, Avg, AreaAgent),
	def("sermon_agent_buffer_pending_batches", UnitCount, Max, AreaAgent),
	def("sermon_agent_buffer_bytes", UnitBytes, Max, AreaAgent),
	def("sermon_agent_buffer_dropped_batches", UnitCount, Last, AreaAgent),
	def("sermon_agent_series_dropped_samples", UnitCount, Last, AreaAgent),
	def("sermon_agent_journal_lost_entries", UnitCount, Last, AreaAgent),
	def("sermon_agent_journal_invalid_lines", UnitCount, Last, AreaAgent),
	// Log shipping (M3-04)
	def("sermon_agent_logs_sent_entries", UnitCount, Last, AreaAgent),
	def("sermon_agent_logs_dropped_entries", UnitCount, Last, AreaAgent, "reason"),
	def("sermon_agent_logs_redacted_entries", UnitCount, Last, AreaAgent),
	// Log files of the groups' globs (M3-05)
	def("sermon_agent_log_files_followed", UnitCount, Last, AreaAgent),
	def("sermon_agent_log_files_rejected", UnitCount, Last, AreaAgent),

	// Updates, derived by the server from the node's update report (M4-16)
	server(def("sermon_updates_pending", UnitCount, Last, AreaSystem)),
	server(def("sermon_updates_security_pending", UnitCount, Last, AreaSystem)),
	server(def("sermon_updates_security_age_seconds", UnitSeconds, Last, AreaSystem)),
	server(def("sermon_updates_reboot_required", UnitBoolean, Max, AreaSystem)),
	server(def("sermon_updates_reboot_required_seconds", UnitSeconds, Last, AreaSystem)),
}

func server(d Definition) Definition {
	d.Server = true
	return d
}

var byName = func() map[string]Definition {
	m := make(map[string]Definition, len(definitions))
	for _, d := range definitions {
		m[d.Name] = d
	}
	return m
}()

// Lookup returns the definition of a metric.
func Lookup(name string) (Definition, bool) {
	d, ok := byName[name]
	return d, ok
}

// All returns every definition, sorted by name.
func All() []Definition {
	all := slices.Clone(definitions)
	slices.SortFunc(all, func(a, b Definition) int { return strings.Compare(a.Name, b.Name) })
	return all
}
