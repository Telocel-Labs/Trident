//! Reference escrow (issue #258).
//!
//! Holds a fixed amount of a SEP-41 token in trust for a beneficiary, moved
//! there by an arbiter's decision: `release` pays the beneficiary, `refund`
//! returns the funds to the depositor. Deliberately small — no partial
//! release, no timeouts. See issue #277 for full-depth escrow work.
#![no_std]

use soroban_sdk::{
    contract, contracterror, contractimpl, contracttype, symbol_short, Address, Env,
};

#[derive(Clone)]
#[contracttype]
enum DataKey {
    Depositor,
    Beneficiary,
    Arbiter,
    Token,
    Amount,
    Deposited,
    Settled,
}

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum EscrowError {
    NotDeposited = 1,
    AlreadySettled = 2,
}

#[contract]
pub struct Escrow;

#[contractimpl]
impl Escrow {
    /// One-time setup. Must be called before `deposit`.
    pub fn initialize(
        env: Env,
        depositor: Address,
        beneficiary: Address,
        arbiter: Address,
        token: Address,
        amount: i128,
    ) {
        env.storage()
            .instance()
            .set(&DataKey::Depositor, &depositor);
        env.storage()
            .instance()
            .set(&DataKey::Beneficiary, &beneficiary);
        env.storage().instance().set(&DataKey::Arbiter, &arbiter);
        env.storage().instance().set(&DataKey::Token, &token);
        env.storage().instance().set(&DataKey::Amount, &amount);
        env.storage().instance().set(&DataKey::Deposited, &false);
        env.storage().instance().set(&DataKey::Settled, &false);
    }

    /// Pull the escrowed amount from the depositor into this contract.
    pub fn deposit(env: Env) {
        let depositor: Address = env.storage().instance().get(&DataKey::Depositor).unwrap();
        depositor.require_auth();

        let token: Address = env.storage().instance().get(&DataKey::Token).unwrap();
        let amount: i128 = env.storage().instance().get(&DataKey::Amount).unwrap();
        soroban_sdk::token::TokenClient::new(&env, &token).transfer(
            &depositor,
            &env.current_contract_address(),
            &amount,
        );

        env.storage().instance().set(&DataKey::Deposited, &true);
        env.events().publish((symbol_short!("deposited"),), amount);
    }

    /// Arbiter releases the escrowed funds to the beneficiary.
    pub fn release(env: Env) -> Result<(), EscrowError> {
        let arbiter: Address = env.storage().instance().get(&DataKey::Arbiter).unwrap();
        arbiter.require_auth();
        Self::settle(&env, DataKey::Beneficiary)?;
        env.events().publish((symbol_short!("released"),), ());
        Ok(())
    }

    /// Arbiter refunds the escrowed funds back to the depositor.
    pub fn refund(env: Env) -> Result<(), EscrowError> {
        let arbiter: Address = env.storage().instance().get(&DataKey::Arbiter).unwrap();
        arbiter.require_auth();
        Self::settle(&env, DataKey::Depositor)?;
        env.events().publish((symbol_short!("refunded"),), ());
        Ok(())
    }

    fn settle(env: &Env, recipient_key: DataKey) -> Result<(), EscrowError> {
        let deposited: bool = env.storage().instance().get(&DataKey::Deposited).unwrap();
        if !deposited {
            return Err(EscrowError::NotDeposited);
        }
        let settled: bool = env.storage().instance().get(&DataKey::Settled).unwrap();
        if settled {
            return Err(EscrowError::AlreadySettled);
        }

        let token: Address = env.storage().instance().get(&DataKey::Token).unwrap();
        let amount: i128 = env.storage().instance().get(&DataKey::Amount).unwrap();
        let recipient: Address = env.storage().instance().get(&recipient_key).unwrap();

        soroban_sdk::token::TokenClient::new(env, &token).transfer(
            &env.current_contract_address(),
            &recipient,
            &amount,
        );
        env.storage().instance().set(&DataKey::Settled, &true);
        Ok(())
    }
}

#[cfg(test)]
mod test {
    use super::*;
    use soroban_sdk::testutils::Address as _;
    use soroban_sdk::String;

    /// Deploy the workspace's own reference token contract and mint
    /// `amount` to `holder`, for a real (not mocked) cross-contract escrow
    /// test.
    fn deploy_funded_token(env: &Env, holder: &Address, amount: i128) -> Address {
        let admin = Address::generate(env);
        let token_id = env.register(token::Token, ());
        let token_client = token::TokenClient::new(env, &token_id);
        token_client.initialize(
            &admin,
            &7,
            &String::from_str(env, "Escrow Test Token"),
            &String::from_str(env, "ETT"),
        );
        token_client.mint(holder, &amount);
        token_id
    }

    #[test]
    fn release_before_deposit_fails() {
        let env = Env::default();
        env.mock_all_auths();

        let depositor = Address::generate(&env);
        let beneficiary = Address::generate(&env);
        let arbiter = Address::generate(&env);
        let token = deploy_funded_token(&env, &depositor, 1_000);

        let contract_id = env.register(Escrow, ());
        let client = EscrowClient::new(&env, &contract_id);
        client.initialize(&depositor, &beneficiary, &arbiter, &token, &1_000);

        let result = client.try_release();
        assert!(result.is_err());
    }

    #[test]
    fn deposit_then_release_pays_beneficiary() {
        let env = Env::default();
        env.mock_all_auths();

        let depositor = Address::generate(&env);
        let beneficiary = Address::generate(&env);
        let arbiter = Address::generate(&env);
        let token = deploy_funded_token(&env, &depositor, 1_000);
        let token_client = token::TokenClient::new(&env, &token);

        let contract_id = env.register(Escrow, ());
        let client = EscrowClient::new(&env, &contract_id);
        client.initialize(&depositor, &beneficiary, &arbiter, &token, &1_000);

        client.deposit();
        assert_eq!(token_client.balance(&depositor), 0);
        assert_eq!(token_client.balance(&contract_id), 1_000);

        client.release();
        assert_eq!(token_client.balance(&beneficiary), 1_000);
        assert_eq!(token_client.balance(&contract_id), 0);

        // A settled escrow cannot be released or refunded again.
        assert!(client.try_release().is_err());
        assert!(client.try_refund().is_err());
    }

    #[test]
    fn deposit_then_refund_returns_to_depositor() {
        let env = Env::default();
        env.mock_all_auths();

        let depositor = Address::generate(&env);
        let beneficiary = Address::generate(&env);
        let arbiter = Address::generate(&env);
        let token = deploy_funded_token(&env, &depositor, 1_000);
        let token_client = token::TokenClient::new(&env, &token);

        let contract_id = env.register(Escrow, ());
        let client = EscrowClient::new(&env, &contract_id);
        client.initialize(&depositor, &beneficiary, &arbiter, &token, &1_000);

        client.deposit();
        client.refund();

        assert_eq!(token_client.balance(&depositor), 1_000);
        assert_eq!(token_client.balance(&beneficiary), 0);
    }
}
