//! The root helper decodes what the agent sends and applies its rules as root (ADR 0035, M4-02):
//! no input may panic, start a run with an invalid ID or reason, slip past the veto or put an
//! unchecked ID into the journal. Input: one byte with the switches of helper.toml, then the
//! request as sent on the socket (without its length).
#![no_main]

use libfuzzer_sys::fuzz_target;
use prost::Message;
use sermon_helper::config::Config;
use sermon_helper::request::{self, Executor, Failure, Run, valid_reason, valid_run_id};
use sermon_helper_proto::v1::{HelperRequest, RunState, StatusInfo, Updates};

/// Accepts every run and checks it.
struct Checker {
    config: Config,
}

impl Executor for Checker {
    fn status(&self) -> StatusInfo {
        StatusInfo::default()
    }
    fn list_updates(&mut self, _: bool) -> Result<Updates, Failure> {
        Ok(Updates::default())
    }
    fn start(&mut self, run: &Run) -> Result<(), Failure> {
        match run {
            Run::Updates { id, .. } => {
                assert!(valid_run_id(id) && self.config.allow_remote_updates)
            }
            Run::Reboot { id, reason } => {
                assert!(
                    valid_run_id(id) && valid_reason(reason) && self.config.allow_remote_reboot
                );
            }
            Run::SelfUpdate { id } => assert!(valid_run_id(id) && self.config.allow_self_update),
        }
        Ok(())
    }
    fn result(&self, id: &str) -> RunState {
        assert!(valid_run_id(id));
        RunState::default()
    }
}

fuzz_target!(|data: &[u8]| {
    let Some((&switches, body)) = data.split_first() else {
        return;
    };
    let config = Config {
        allow_remote_updates: switches & 1 != 0,
        allow_remote_reboot: switches & 2 != 0,
        allow_self_update: switches & 4 != 0,
    };
    let Ok(req) = HelperRequest::decode(body) else {
        return;
    };
    let (response, audit) = request::handle(&req, config, &mut Checker { config });
    assert!(audit.run_id.is_empty() || valid_run_id(&audit.run_id));
    assert!(response.encode_to_vec().len() < 4096);
});
