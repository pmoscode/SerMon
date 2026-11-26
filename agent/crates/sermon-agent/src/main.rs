//! `SerMon` agent binary: parses the command line and hands over to the library.

use std::process::ExitCode;

use clap::Parser;
use sermon_agent::cli::Cli;

/// musl's allocator (mallocng) hands memory the agent freed back only in whole groups: over
/// hours the agent kept 26 MiB for 1 MiB of live data (M4-32). jemalloc with one arena, no thread
/// cache and a decay of a second (agent/.cargo/config.toml) stays at a fixed few MiB (ADR 0038).
#[cfg(all(target_os = "linux", target_env = "musl"))]
#[global_allocator]
static ALLOCATOR: tikv_jemallocator::Jemalloc = tikv_jemallocator::Jemalloc;

fn main() -> ExitCode {
    // clap prints help, version and usage errors itself (exit code 0 or 2).
    let cli = Cli::parse();
    match sermon_agent::execute(cli) {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("sermon-agent: {error:#}");
            ExitCode::FAILURE
        }
    }
}
