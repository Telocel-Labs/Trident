//! Counter — the simplest possible reference contract (issue #258).
//!
//! Stores a single `u32` in instance storage and increments it. Used by the
//! E2E suite (issue #268) as a minimal deploy/invoke smoke target before
//! exercising the heavier token contract.
#![no_std]

use soroban_sdk::{contract, contractimpl, contracttype, Env};

#[contracttype]
enum DataKey {
    Count,
}

#[contract]
pub struct Counter;

#[contractimpl]
impl Counter {
    /// Increment the stored count and return the new value.
    pub fn increment(env: Env) -> u32 {
        let mut count: u32 = env.storage().instance().get(&DataKey::Count).unwrap_or(0);
        count += 1;
        env.storage().instance().set(&DataKey::Count, &count);
        count
    }

    /// Return the current count without modifying it.
    pub fn count(env: Env) -> u32 {
        env.storage().instance().get(&DataKey::Count).unwrap_or(0)
    }
}

#[cfg(test)]
mod test {
    use super::*;

    #[test]
    fn increment_returns_running_total() {
        let env = Env::default();
        let contract_id = env.register(Counter, ());
        let client = CounterClient::new(&env, &contract_id);

        assert_eq!(client.count(), 0);
        assert_eq!(client.increment(), 1);
        assert_eq!(client.increment(), 2);
        assert_eq!(client.count(), 2);
    }
}
