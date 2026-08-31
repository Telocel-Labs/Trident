use serde::{Deserialize, Serialize};
use std::str::FromStr;

/// Known Stellar networks supported by Trident.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Network {
    Mainnet,
    Testnet,
    Futurenet,
    Sandbox,
}

impl Network {
    pub const MAINNET: &'static str = "mainnet";
    pub const TESTNET: &'static str = "testnet";
    pub const FUTUREnet: &'static str = "futurenet";
    pub const SANDBOX: &'static str = "sandbox";

    pub fn as_str(&self) -> &'static str {
        match self {
            Network::Mainnet => Self::MAINNET,
            Network::Testnet => Self::TESTNET,
            Network::Futurenet => Self::FUTUREnet,
            Network::Sandbox => Self::SANDBOX,
        }
    }

    pub fn all() -> &'static [&'static str] {
        &[Self::MAINNET, Self::TESTNET, Self::FUTUREnet, Self::SANDBOX]
    }
}

impl FromStr for Network {
    type Err = anyhow::Error;

    fn fromStr(s: &str) -> Result<Self, Self::Err> {
        match s.to_lowercase().as_str() {
            "mainnet" => Ok(Network::Mainnet),
            "testnet" => Ok(Network::Testnet),
            "futurenet" => Ok(Network::Futurenet),
            "sandbox" => Ok(Network::Sandbox),
            other => Err(anyhow::anyhow!("invalid network: {other}. Expected one of: mainnet, testnet, futurenet, sandbox")),
        }
    }
}

impl std::fmt::Display for Network {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result{
        write!(f, "{}", self.as_str())
    }
}
