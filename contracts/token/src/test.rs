#![cfg(test)]

use soroban_sdk::{testutils::Address as _, Address, Env, String};

use crate::{Token, TokenClient};

fn setup<'a>(env: &Env) -> (Address, TokenClient<'a>, Address) {
    let admin = Address::generate(env);
    let contract_id = env.register(Token, ());
    let client = TokenClient::new(env, &contract_id);
    client.initialize(&admin, &7, &String::from_str(env, "Trident Reference Token"), &String::from_str(env, "TRT"));
    (admin, client, contract_id)
}

#[test]
fn mint_and_transfer_update_balances() {
    let env = Env::default();
    env.mock_all_auths();

    let (admin, client, _) = setup(&env);
    let alice = Address::generate(&env);
    let bob = Address::generate(&env);

    client.mint(&alice, &1_000);
    assert_eq!(client.balance(&alice), 1_000);
    assert_eq!(client.admin(), admin);

    client.transfer(&alice, &bob, &400);
    assert_eq!(client.balance(&alice), 600);
    assert_eq!(client.balance(&bob), 400);
}

#[test]
fn approve_then_transfer_from_spends_allowance() {
    let env = Env::default();
    env.mock_all_auths();

    let (_, client, _) = setup(&env);
    let alice = Address::generate(&env);
    let bob = Address::generate(&env);
    let spender = Address::generate(&env);

    client.mint(&alice, &500);
    client.approve(&alice, &spender, &200, &(env.ledger().sequence() + 100));
    assert_eq!(client.allowance(&alice, &spender), 200);

    client.transfer_from(&spender, &alice, &bob, &150);
    assert_eq!(client.balance(&alice), 350);
    assert_eq!(client.balance(&bob), 150);
    assert_eq!(client.allowance(&alice, &spender), 50);
}

#[test]
fn burn_reduces_balance() {
    let env = Env::default();
    env.mock_all_auths();

    let (_, client, _) = setup(&env);
    let alice = Address::generate(&env);

    client.mint(&alice, &300);
    client.burn(&alice, &100);
    assert_eq!(client.balance(&alice), 200);
}

#[test]
fn metadata_round_trips() {
    let env = Env::default();
    env.mock_all_auths();

    let (_, client, _) = setup(&env);
    assert_eq!(client.decimals(), 7);
    assert_eq!(client.name(), String::from_str(&env, "Trident Reference Token"));
    assert_eq!(client.symbol(), String::from_str(&env, "TRT"));
}
