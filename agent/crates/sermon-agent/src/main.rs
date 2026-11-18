//! `SerMon` agent: collects metrics, logs and security events and streams them to the server.
//!
//! The full CLI (`enroll`, `run`, `uninstall`, `simulate`) follows in M1-12.

use std::process::ExitCode;

const NAME: &str = "sermon-agent";
const USAGE: &str = "usage: sermon-agent --version";

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args.as_slice() {
        [flag] if flag == "--version" || flag == "-V" => {
            println!("{NAME} {}", env!("CARGO_PKG_VERSION"));
            ExitCode::SUCCESS
        }
        _ => {
            eprintln!("{USAGE}");
            ExitCode::from(2)
        }
    }
}
