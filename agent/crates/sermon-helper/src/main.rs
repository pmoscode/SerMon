//! `SerMon` root helper: executes a fixed set of privileged operations (package updates, reboot) for the agent.
//!
//! Socket server, peer credential check and operations follow in M4 (E4.1).

use std::process::ExitCode;

const NAME: &str = "sermon-helper";
const USAGE: &str = "usage: sermon-helper --version";

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
