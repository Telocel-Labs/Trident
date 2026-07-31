//! Reference non-fungible token (issue #258).
//!
//! Deliberately small: sequential `u32` token ids, single owner per token, no
//! approvals/metadata URIs. See issue #275 for full-depth NFT work.
#![no_std]

use soroban_sdk::{
    contract, contracterror, contractimpl, contracttype, symbol_short, Address, Env,
};

#[derive(Clone)]
#[contracttype]
enum DataKey {
    Owner(u32),
    NextId,
}

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum NftError {
    NotOwner = 1,
}

#[contract]
pub struct Nft;

#[contractimpl]
impl Nft {
    /// Mint the next sequential token id to `to`. Returns the new token id.
    pub fn mint(env: Env, to: Address) -> u32 {
        let token_id: u32 = env.storage().instance().get(&DataKey::NextId).unwrap_or(0);
        env.storage()
            .instance()
            .set(&DataKey::NextId, &(token_id + 1));
        env.storage()
            .persistent()
            .set(&DataKey::Owner(token_id), &to);

        env.events().publish((symbol_short!("mint"), to), token_id);
        token_id
    }

    pub fn owner_of(env: Env, token_id: u32) -> Address {
        env.storage()
            .persistent()
            .get(&DataKey::Owner(token_id))
            .expect("no such token")
    }

    /// Move `token_id` from the caller (`from`) to `to`.
    pub fn transfer(env: Env, from: Address, to: Address, token_id: u32) -> Result<(), NftError> {
        from.require_auth();

        let owner: Address = env
            .storage()
            .persistent()
            .get(&DataKey::Owner(token_id))
            .expect("no such token");
        if owner != from {
            return Err(NftError::NotOwner);
        }

        env.storage()
            .persistent()
            .set(&DataKey::Owner(token_id), &to);

        env.events()
            .publish((symbol_short!("transfer"), from, to), token_id);
        Ok(())
    }
}

#[cfg(test)]
mod test {
    use super::*;
    use soroban_sdk::testutils::Address as _;

    #[test]
    fn mint_then_transfer_changes_owner() {
        let env = Env::default();
        env.mock_all_auths();
        let contract_id = env.register(Nft, ());
        let client = NftClient::new(&env, &contract_id);

        let alice = Address::generate(&env);
        let bob = Address::generate(&env);

        let token_id = client.mint(&alice);
        assert_eq!(client.owner_of(&token_id), alice);

        client.transfer(&alice, &bob, &token_id);
        assert_eq!(client.owner_of(&token_id), bob);
    }

    #[test]
    fn transfer_by_non_owner_fails() {
        let env = Env::default();
        env.mock_all_auths();
        let contract_id = env.register(Nft, ());
        let client = NftClient::new(&env, &contract_id);

        let alice = Address::generate(&env);
        let bob = Address::generate(&env);
        let mallory = Address::generate(&env);

        let token_id = client.mint(&alice);
        let result = client.try_transfer(&mallory, &bob, &token_id);
        assert!(result.is_err());
    }
}
