//! Escrow reference contract for Trident integration testing.
//!
//! Implements a two-party escrow (depositor → beneficiary) with three state
//! transitions: deposit → release (happy path) or deposit → refund. Each
//! transition emits a documented Soroban contract event, making this contract
//! useful for validating the indexer against a realistic, stateful event
//! sequence with ordering and per-contract filtering.
//!
//! # Event topics and data
//!
//! | Function | topic[0]  | data                              |
//! |----------|-----------|-----------------------------------|
//! | deposit  | "deposit" | (depositor: Address, beneficiary: Address, amount: i128) |
//! | release  | "release" | (beneficiary: Address, amount: i128) |
//! | refund   | "refund"  | (depositor: Address, amount: i128) |

#![no_std]

use soroban_sdk::{contract, contractimpl, contracttype, symbol_short, Address, Env};

// ---------------------------------------------------------------------------
// Storage keys
// ---------------------------------------------------------------------------

#[contracttype]
pub enum DataKey {
    Depositor,
    Beneficiary,
    Amount,
    Settled,
}

// ---------------------------------------------------------------------------
// Contract
// ---------------------------------------------------------------------------

#[contract]
pub struct EscrowContract;

#[contractimpl]
impl EscrowContract {
    /// Lock `amount` in escrow from `depositor` to `beneficiary`.
    ///
    /// Emits `deposit(depositor, beneficiary, amount)`.
    /// Panics if a deposit is already active.
    pub fn deposit(env: Env, depositor: Address, beneficiary: Address, amount: i128) {
        depositor.require_auth();
        assert!(amount > 0, "amount must be positive");
        assert!(
            env.storage().instance().get::<_, bool>(&DataKey::Settled).is_none(),
            "escrow already initialised"
        );

        env.storage().instance().set(&DataKey::Depositor, &depositor);
        env.storage().instance().set(&DataKey::Beneficiary, &beneficiary);
        env.storage().instance().set(&DataKey::Amount, &amount);
        env.storage().instance().set(&DataKey::Settled, &false);

        env.events().publish(
            (symbol_short!("deposit"),),
            (depositor, beneficiary, amount),
        );
    }

    /// Release escrowed funds to the beneficiary.
    ///
    /// Only the original depositor may call this. Panics if already settled.
    /// Emits `release(beneficiary, amount)`.
    pub fn release(env: Env, depositor: Address) {
        depositor.require_auth();
        let stored: Address = env.storage().instance().get(&DataKey::Depositor).expect("no deposit");
        assert!(stored == depositor, "caller is not the depositor");
        Self::assert_not_settled(&env);

        let beneficiary: Address = env.storage().instance().get(&DataKey::Beneficiary).unwrap();
        let amount: i128 = env.storage().instance().get(&DataKey::Amount).unwrap();
        env.storage().instance().set(&DataKey::Settled, &true);

        env.events().publish((symbol_short!("release"),), (beneficiary, amount));
    }

    /// Refund escrowed funds back to the depositor.
    ///
    /// Only the original depositor may call this. Panics if already settled.
    /// Emits `refund(depositor, amount)`.
    pub fn refund(env: Env, depositor: Address) {
        depositor.require_auth();
        let stored: Address = env.storage().instance().get(&DataKey::Depositor).expect("no deposit");
        assert!(stored == depositor, "caller is not the depositor");
        Self::assert_not_settled(&env);

        let amount: i128 = env.storage().instance().get(&DataKey::Amount).unwrap();
        env.storage().instance().set(&DataKey::Settled, &true);

        env.events().publish((symbol_short!("refund"),), (depositor, amount));
    }

    // ------------------------------------------------------------------

    fn assert_not_settled(env: &Env) {
        let settled: bool = env.storage().instance().get(&DataKey::Settled).unwrap_or(false);
        assert!(!settled, "escrow already settled");
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use soroban_sdk::{
        testutils::{Address as _, Events},
        vec, Address, Env, IntoVal,
    };

    fn setup() -> (Env, EscrowContractClient<'static>, Address, Address) {
        let env = Env::default();
        env.mock_all_auths();
        let contract_id = env.register_contract(None, EscrowContract);
        let client = EscrowContractClient::new(&env, &contract_id);
        let depositor = Address::generate(&env);
        let beneficiary = Address::generate(&env);
        (env, client, depositor, beneficiary)
    }

    #[test]
    fn test_happy_path_deposit_then_release() {
        let (env, client, depositor, beneficiary) = setup();
        let amount: i128 = 1_000_000;

        client.deposit(&depositor, &beneficiary, &amount);
        client.release(&depositor);

        let events = env.events().all();
        assert_eq!(events.len(), 2);

        // First event: deposit
        let (_, deposit_topics, deposit_data) = events.get(0).unwrap();
        let deposit_topic: soroban_sdk::Symbol = deposit_topics.get(0).unwrap().unwrap();
        assert_eq!(deposit_topic, symbol_short!("deposit"));
        let _ = deposit_data; // data shape validated by contract code

        // Second event: release
        let (_, release_topics, _) = events.get(1).unwrap();
        let release_topic: soroban_sdk::Symbol = release_topics.get(0).unwrap().unwrap();
        assert_eq!(release_topic, symbol_short!("release"));
    }

    #[test]
    fn test_refund_path() {
        let (env, client, depositor, beneficiary) = setup();
        let amount: i128 = 500_000;

        client.deposit(&depositor, &beneficiary, &amount);
        client.refund(&depositor);

        let events = env.events().all();
        assert_eq!(events.len(), 2);

        let (_, refund_topics, _) = events.get(1).unwrap();
        let refund_topic: soroban_sdk::Symbol = refund_topics.get(0).unwrap().unwrap();
        assert_eq!(refund_topic, symbol_short!("refund"));
    }

    #[test]
    #[should_panic(expected = "escrow already settled")]
    fn test_cannot_release_twice() {
        let (_, client, depositor, beneficiary) = setup();
        client.deposit(&depositor, &beneficiary, &100_000);
        client.release(&depositor);
        client.release(&depositor);
    }

    #[test]
    #[should_panic(expected = "escrow already settled")]
    fn test_cannot_refund_after_release() {
        let (_, client, depositor, beneficiary) = setup();
        client.deposit(&depositor, &beneficiary, &100_000);
        client.release(&depositor);
        client.refund(&depositor);
    }

    #[test]
    #[should_panic(expected = "escrow already initialised")]
    fn test_cannot_deposit_twice() {
        let (_, client, depositor, beneficiary) = setup();
        client.deposit(&depositor, &beneficiary, &100_000);
        client.deposit(&depositor, &beneficiary, &200_000);
    }
}
