//! `SerMon` agent binary: parses the command line and hands over to the library.

use std::process::ExitCode;

use clap::Parser;
use sermon_agent::cli::Cli;

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
