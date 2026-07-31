//! Reference SEP-41 token contract (issue #267).
//!
//! A minimal, standards-conformant SEP-41 fungible token used to give the
//! indexer's E2E tests, deploy scripts, and interface-detection tests
//! (issue #269) a deterministic on-chain contract to exercise. Storage
//! layout mirrors soroban-examples' token contract (`Balance(Address)` /
//! `Allowance{from,spender}`) so contract_storage_snapshots (issue #270) can
//! read the `Balance(Address)` key of any deployment of this contract.
#![no_std]

use soroban_sdk::{contract, contractimpl, contracttype, Address, Env, String, Symbol};

#[derive(Clone)]
#[contracttype]
pub struct AllowanceDataKey {
    pub from: Address,
    pub spender: Address,
}

#[derive(Clone)]
#[contracttype]
pub struct AllowanceValue {
    pub amount: i128,
    pub expiration_ledger: u32,
}

#[derive(Clone)]
#[contracttype]
pub enum DataKey {
    Admin,
    Decimals,
    Name,
    Symbol,
    Balance(Address),
    Allowance(AllowanceDataKey),
}

fn read_balance(env: &Env, addr: Address) -> i128 {
    env.storage()
        .persistent()
        .get(&DataKey::Balance(addr))
        .unwrap_or(0)
}

fn write_balance(env: &Env, addr: Address, amount: i128) {
    env.storage()
        .persistent()
        .set(&DataKey::Balance(addr), &amount);
}

fn read_allowance(env: &Env, from: Address, spender: Address) -> AllowanceValue {
    let key = DataKey::Allowance(AllowanceDataKey { from, spender });
    env.storage().temporary().get(&key).unwrap_or(AllowanceValue {
        amount: 0,
        expiration_ledger: 0,
    })
}

fn write_allowance(env: &Env, from: Address, spender: Address, amount: i128, expiration_ledger: u32) {
    let key = DataKey::Allowance(AllowanceDataKey { from, spender });
    env.storage().temporary().set(
        &key,
        &AllowanceValue {
            amount,
            expiration_ledger,
        },
    );
}

fn spend_allowance(env: &Env, from: Address, spender: Address, amount: i128) {
    let allowance = read_allowance(env, from.clone(), spender.clone());
    if allowance.amount < amount {
        panic!("insufficient allowance");
    }
    if amount > 0 {
        write_allowance(
            env,
            from,
            spender,
            allowance.amount - amount,
            allowance.expiration_ledger,
        );
    }
}

fn read_admin(env: &Env) -> Address {
    env.storage()
        .instance()
        .get(&DataKey::Admin)
        .expect("contract not initialized")
}

fn require_admin(env: &Env) {
    read_admin(env).require_auth();
}

fn require_non_negative(amount: i128) {
    if amount < 0 {
        panic!("amount must be non-negative");
    }
}

#[contract]
pub struct Token;

#[contractimpl]
impl Token {
    pub fn initialize(env: Env, admin: Address, decimal: u32, name: String, symbol: String) {
        if env.storage().instance().has(&DataKey::Admin) {
            panic!("already initialized");
        }
        env.storage().instance().set(&DataKey::Admin, &admin);
        env.storage().instance().set(&DataKey::Decimals, &decimal);
        env.storage().instance().set(&DataKey::Name, &name);
        env.storage().instance().set(&DataKey::Symbol, &symbol);
    }

    pub fn mint(env: Env, to: Address, amount: i128) {
        require_admin(&env);
        require_non_negative(amount);
        let balance = read_balance(&env, to.clone());
        write_balance(&env, to.clone(), balance + amount);
        env.events()
            .publish((Symbol::new(&env, "mint"), read_admin(&env), to), amount);
    }

    pub fn set_admin(env: Env, new_admin: Address) {
        require_admin(&env);
        env.storage().instance().set(&DataKey::Admin, &new_admin);
    }

    pub fn admin(env: Env) -> Address {
        read_admin(&env)
    }

    pub fn allowance(env: Env, from: Address, spender: Address) -> i128 {
        read_allowance(&env, from, spender).amount
    }

    pub fn approve(env: Env, from: Address, spender: Address, amount: i128, expiration_ledger: u32) {
        from.require_auth();
        require_non_negative(amount);
        write_allowance(&env, from.clone(), spender.clone(), amount, expiration_ledger);
        env.events().publish(
            (Symbol::new(&env, "approve"), from, spender),
            (amount, expiration_ledger),
        );
    }

    pub fn balance(env: Env, id: Address) -> i128 {
        read_balance(&env, id)
    }

    pub fn transfer(env: Env, from: Address, to: Address, amount: i128) {
        from.require_auth();
        require_non_negative(amount);
        let from_balance = read_balance(&env, from.clone());
        if from_balance < amount {
            panic!("insufficient balance");
        }
        write_balance(&env, from.clone(), from_balance - amount);
        let to_balance = read_balance(&env, to.clone());
        write_balance(&env, to.clone(), to_balance + amount);
        env.events()
            .publish((Symbol::new(&env, "transfer"), from, to), amount);
    }

    pub fn transfer_from(env: Env, spender: Address, from: Address, to: Address, amount: i128) {
        spender.require_auth();
        spend_allowance(&env, from.clone(), spender, amount);
        require_non_negative(amount);
        let from_balance = read_balance(&env, from.clone());
        if from_balance < amount {
            panic!("insufficient balance");
        }
        write_balance(&env, from.clone(), from_balance - amount);
        let to_balance = read_balance(&env, to.clone());
        write_balance(&env, to.clone(), to_balance + amount);
        env.events()
            .publish((Symbol::new(&env, "transfer"), from, to), amount);
    }

    pub fn burn(env: Env, from: Address, amount: i128) {
        from.require_auth();
        require_non_negative(amount);
        let from_balance = read_balance(&env, from.clone());
        if from_balance < amount {
            panic!("insufficient balance");
        }
        write_balance(&env, from.clone(), from_balance - amount);
        env.events().publish((Symbol::new(&env, "burn"), from), amount);
    }

    pub fn burn_from(env: Env, spender: Address, from: Address, amount: i128) {
        spender.require_auth();
        spend_allowance(&env, from.clone(), spender, amount);
        require_non_negative(amount);
        let from_balance = read_balance(&env, from.clone());
        if from_balance < amount {
            panic!("insufficient balance");
        }
        write_balance(&env, from.clone(), from_balance - amount);
        env.events().publish((Symbol::new(&env, "burn"), from), amount);
    }

    pub fn decimals(env: Env) -> u32 {
        env.storage().instance().get(&DataKey::Decimals).unwrap()
    }

    pub fn name(env: Env) -> String {
        env.storage().instance().get(&DataKey::Name).unwrap()
    }

    pub fn symbol(env: Env) -> String {
        env.storage().instance().get(&DataKey::Symbol).unwrap()
    }
}

#[cfg(test)]
mod test;
