use std::collections::HashMap;

#[test]
fn defaults_match_go_proxy_expectations() {
    let cfg = rust_sync_proxy::config::Config::from_env_map(&HashMap::new()).unwrap();
    assert_eq!(cfg.port, 8787);
    assert_eq!(cfg.upstream_base_url, "https://magic666.top");
    assert_eq!(cfg.image_host_mode.as_str(), "legacy");
}
