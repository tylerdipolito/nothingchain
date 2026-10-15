use anchor_lang::prelude::*;
use anchor_spl::token::{self, Burn, Mint, MintTo, Token, TokenAccount, Transfer};
declare_id!("4TdBWFzDUrn4Y5fB9mNJHWpdPP8PNyPnxY7s7FcN7AtK");
const LOCKED_LP: u64 = 1000;
#[program]
pub mod goamm {
    use super::*;
    pub fn initialize(ctx: Context<Initialize>) -> Result<()> {
        require!(
            ctx.accounts.mint_a.key() < ctx.accounts.mint_b.key(),
            AmmError::MintOrder
        );
        let p = &mut ctx.accounts.pool;
        p.mint_a = ctx.accounts.mint_a.key();
        p.mint_b = ctx.accounts.mint_b.key();
        p.vault_a = ctx.accounts.vault_a.key();
        p.vault_b = ctx.accounts.vault_b.key();
        p.lp_mint = ctx.accounts.lp_mint.key();
        p.bump = ctx.bumps.pool;
        Ok(())
    }
    pub fn deposit(
        ctx: Context<Liquidity>,
        max_a: u64,
        max_b: u64,
        min_shares: u64,
        expires_at_slot: u64,
    ) -> Result<()> {
        check_expiry(expires_at_slot)?;
        require!(
            max_a > 0 && max_b > 0 && min_shares > 0,
            AmmError::ZeroAmount
        );
        let supply = ctx.accounts.lp_mint.supply;
        let (a, b, shares) = deposit_math(
            max_a,
            max_b,
            ctx.accounts.vault_a.amount,
            ctx.accounts.vault_b.amount,
            supply,
        )?;
        require!(shares >= min_shares, AmmError::Slippage);
        ctx.accounts
            .vault_a
            .amount
            .checked_add(a)
            .ok_or(AmmError::Overflow)?;
        ctx.accounts
            .vault_b
            .amount
            .checked_add(b)
            .ok_or(AmmError::Overflow)?;
        supply
            .checked_add(shares)
            .and_then(|x| x.checked_add(if supply == 0 { LOCKED_LP } else { 0 }))
            .ok_or(AmmError::Overflow)?;
        token::transfer(
            CpiContext::new(
                ctx.accounts.token_program.to_account_info(),
                Transfer {
                    from: ctx.accounts.user_a.to_account_info(),
                    to: ctx.accounts.vault_a.to_account_info(),
                    authority: ctx.accounts.user.to_account_info(),
                },
            ),
            a,
        )?;
        token::transfer(
            CpiContext::new(
                ctx.accounts.token_program.to_account_info(),
                Transfer {
                    from: ctx.accounts.user_b.to_account_info(),
                    to: ctx.accounts.vault_b.to_account_info(),
                    authority: ctx.accounts.user.to_account_info(),
                },
            ),
            b,
        )?;
        let p = &ctx.accounts.pool;
        let seeds: &[&[u8]] = &[b"pool", p.mint_a.as_ref(), p.mint_b.as_ref(), &[p.bump]];
        let signer = &[seeds];
        if supply == 0 {
            token::mint_to(
                CpiContext::new_with_signer(
                    ctx.accounts.token_program.to_account_info(),
                    MintTo {
                        mint: ctx.accounts.lp_mint.to_account_info(),
                        to: ctx.accounts.locked_lp.to_account_info(),
                        authority: p.to_account_info(),
                    },
                    signer,
                ),
                LOCKED_LP,
            )?;
        }
        token::mint_to(
            CpiContext::new_with_signer(
                ctx.accounts.token_program.to_account_info(),
                MintTo {
                    mint: ctx.accounts.lp_mint.to_account_info(),
                    to: ctx.accounts.user_lp.to_account_info(),
                    authority: p.to_account_info(),
                },
                signer,
            ),
            shares,
        )?;
        emit!(LiquidityEvent {
            user: ctx.accounts.user.key(),
            deposit: true,
            a,
            b,
            shares
        });
        Ok(())
    }
    pub fn withdraw(
        ctx: Context<Liquidity>,
        shares: u64,
        min_a: u64,
        min_b: u64,
        expires_at_slot: u64,
    ) -> Result<()> {
        check_expiry(expires_at_slot)?;
        require!(shares > 0 && min_a > 0 && min_b > 0, AmmError::ZeroAmount);
        let a = mul_div(
            shares,
            ctx.accounts.vault_a.amount,
            ctx.accounts.lp_mint.supply,
        )?;
        let b = mul_div(
            shares,
            ctx.accounts.vault_b.amount,
            ctx.accounts.lp_mint.supply,
        )?;
        require!(
            a > 0 && b > 0 && a >= min_a && b >= min_b,
            AmmError::Slippage
        );
        token::burn(
            CpiContext::new(
                ctx.accounts.token_program.to_account_info(),
                Burn {
                    mint: ctx.accounts.lp_mint.to_account_info(),
                    from: ctx.accounts.user_lp.to_account_info(),
                    authority: ctx.accounts.user.to_account_info(),
                },
            ),
            shares,
        )?;
        let p = &ctx.accounts.pool;
        let seeds: &[&[u8]] = &[b"pool", p.mint_a.as_ref(), p.mint_b.as_ref(), &[p.bump]];
        let signer = &[seeds];
        token::transfer(
            CpiContext::new_with_signer(
                ctx.accounts.token_program.to_account_info(),
                Transfer {
                    from: ctx.accounts.vault_a.to_account_info(),
                    to: ctx.accounts.user_a.to_account_info(),
                    authority: p.to_account_info(),
                },
                signer,
            ),
            a,
        )?;
        token::transfer(
            CpiContext::new_with_signer(
                ctx.accounts.token_program.to_account_info(),
                Transfer {
                    from: ctx.accounts.vault_b.to_account_info(),
                    to: ctx.accounts.user_b.to_account_info(),
                    authority: p.to_account_info(),
                },
                signer,
            ),
            b,
        )?;
        emit!(LiquidityEvent {
            user: ctx.accounts.user.key(),
            deposit: false,
            a,
            b,
            shares
        });
        Ok(())
    }
    pub fn swap(
        ctx: Context<Swap>,
        a_to_b: bool,
        amount: u64,
        min_output: u64,
        expires_at_slot: u64,
    ) -> Result<()> {
        check_expiry(expires_at_slot)?;
        require!(min_output > 0, AmmError::ZeroAmount);
        let (r_in, r_out) = if a_to_b {
            (ctx.accounts.vault_a.amount, ctx.accounts.vault_b.amount)
        } else {
            (ctx.accounts.vault_b.amount, ctx.accounts.vault_a.amount)
        };
        let out = swap_math(amount, r_in, r_out)?;
        require!(out >= min_output, AmmError::Slippage);
        r_in.checked_add(amount).ok_or(AmmError::Overflow)?;
        let (source, destination, v_in, v_out) = if a_to_b {
            (
                &ctx.accounts.user_a,
                &ctx.accounts.user_b,
                &ctx.accounts.vault_a,
                &ctx.accounts.vault_b,
            )
        } else {
            (
                &ctx.accounts.user_b,
                &ctx.accounts.user_a,
                &ctx.accounts.vault_b,
                &ctx.accounts.vault_a,
            )
        };
        token::transfer(
            CpiContext::new(
                ctx.accounts.token_program.to_account_info(),
                Transfer {
                    from: source.to_account_info(),
                    to: v_in.to_account_info(),
                    authority: ctx.accounts.user.to_account_info(),
                },
            ),
            amount,
        )?;
        let p = &ctx.accounts.pool;
        let seeds: &[&[u8]] = &[b"pool", p.mint_a.as_ref(), p.mint_b.as_ref(), &[p.bump]];
        token::transfer(
            CpiContext::new_with_signer(
                ctx.accounts.token_program.to_account_info(),
                Transfer {
                    from: v_out.to_account_info(),
                    to: destination.to_account_info(),
                    authority: p.to_account_info(),
                },
                &[seeds],
            ),
            out,
        )?;
        emit!(SwapEvent {
            user: ctx.accounts.user.key(),
            a_to_b,
            input: amount,
            output: out
        });
        Ok(())
    }
}
#[account]
pub struct Pool {
    pub mint_a: Pubkey,
    pub mint_b: Pubkey,
    pub vault_a: Pubkey,
    pub vault_b: Pubkey,
    pub lp_mint: Pubkey,
    pub bump: u8,
}
#[derive(Accounts)]
pub struct Initialize<'info> {
    #[account(mut)]
    pub payer: Signer<'info>,
    pub mint_a: Account<'info, Mint>,
    pub mint_b: Account<'info, Mint>,
    #[account(init,payer=payer,space=8+32*5+1,seeds=[b"pool",mint_a.key().as_ref(),mint_b.key().as_ref()],bump)]
    pub pool: Account<'info, Pool>,
    #[account(init,payer=payer,token::mint=mint_a,token::authority=pool,seeds=[b"vault-a",pool.key().as_ref()],bump)]
    pub vault_a: Account<'info, TokenAccount>,
    #[account(init,payer=payer,token::mint=mint_b,token::authority=pool,seeds=[b"vault-b",pool.key().as_ref()],bump)]
    pub vault_b: Account<'info, TokenAccount>,
    #[account(init,payer=payer,mint::decimals=6,mint::authority=pool,seeds=[b"lp",pool.key().as_ref()],bump)]
    pub lp_mint: Account<'info, Mint>,
    #[account(init,payer=payer,token::mint=lp_mint,token::authority=pool,seeds=[b"locked",pool.key().as_ref()],bump)]
    pub locked_lp: Account<'info, TokenAccount>,
    pub token_program: Program<'info, Token>,
    pub system_program: Program<'info, System>,
    pub rent: Sysvar<'info, Rent>,
}
#[derive(Accounts)]
pub struct Liquidity<'info> {
    pub user: Signer<'info>,
    #[account(seeds=[b"pool",pool.mint_a.as_ref(),pool.mint_b.as_ref()],bump=pool.bump)]
    pub pool: Account<'info, Pool>,
    #[account(mut,address=pool.vault_a,token::mint=pool.mint_a,token::authority=pool)]
    pub vault_a: Account<'info, TokenAccount>,
    #[account(mut,address=pool.vault_b,token::mint=pool.mint_b,token::authority=pool)]
    pub vault_b: Account<'info, TokenAccount>,
    #[account(mut,address=pool.lp_mint,mint::authority=pool)]
    pub lp_mint: Account<'info, Mint>,
    #[account(mut,seeds=[b"locked",pool.key().as_ref()],bump,token::mint=lp_mint,token::authority=pool)]
    pub locked_lp: Account<'info, TokenAccount>,
    #[account(mut,token::mint=pool.mint_a,token::authority=user)]
    pub user_a: Account<'info, TokenAccount>,
    #[account(mut,token::mint=pool.mint_b,token::authority=user)]
    pub user_b: Account<'info, TokenAccount>,
    #[account(mut,token::mint=lp_mint,token::authority=user)]
    pub user_lp: Account<'info, TokenAccount>,
    pub token_program: Program<'info, Token>,
}
#[derive(Accounts)]
pub struct Swap<'info> {
    pub user: Signer<'info>,
    #[account(seeds=[b"pool",pool.mint_a.as_ref(),pool.mint_b.as_ref()],bump=pool.bump)]
    pub pool: Account<'info, Pool>,
    #[account(mut,address=pool.vault_a,token::mint=pool.mint_a,token::authority=pool)]
    pub vault_a: Account<'info, TokenAccount>,
    #[account(mut,address=pool.vault_b,token::mint=pool.mint_b,token::authority=pool)]
    pub vault_b: Account<'info, TokenAccount>,
    #[account(mut,token::mint=pool.mint_a,token::authority=user)]
    pub user_a: Account<'info, TokenAccount>,
    #[account(mut,token::mint=pool.mint_b,token::authority=user)]
    pub user_b: Account<'info, TokenAccount>,
    pub token_program: Program<'info, Token>,
}
fn check_expiry(slot: u64) -> Result<()> {
    require!(Clock::get()?.slot <= slot, AmmError::Expired);
    Ok(())
}
fn mul_div(a: u64, b: u64, c: u64) -> Result<u64> {
    require!(c > 0, AmmError::EmptyPool);
    u64::try_from(
        u128::from(a)
            .checked_mul(u128::from(b))
            .ok_or(AmmError::Overflow)?
            / u128::from(c),
    )
    .map_err(|_| error!(AmmError::Overflow))
}
fn ceil_div(a: u64, b: u64, c: u64) -> Result<u64> {
    require!(c > 0, AmmError::EmptyPool);
    let n = u128::from(a)
        .checked_mul(u128::from(b))
        .and_then(|n| n.checked_add(u128::from(c) - 1))
        .ok_or(AmmError::Overflow)?;
    u64::try_from(n / u128::from(c)).map_err(|_| error!(AmmError::Overflow))
}
pub fn swap_math(amount: u64, r_in: u64, r_out: u64) -> Result<u64> {
    require!(amount > 0 && r_in > 0 && r_out > 0, AmmError::EmptyPool);
    let effective = u128::from(amount)
        .checked_mul(997)
        .ok_or(AmmError::Overflow)?;
    let numerator = effective
        .checked_mul(u128::from(r_out))
        .ok_or(AmmError::Overflow)?;
    let denominator = u128::from(r_in)
        .checked_mul(1000)
        .and_then(|x| x.checked_add(effective))
        .ok_or(AmmError::Overflow)?;
    let out = u64::try_from(numerator / denominator).map_err(|_| error!(AmmError::Overflow))?;
    require!(out > 0 && out < r_out, AmmError::ZeroAmount);
    Ok(out)
}
fn sqrt(n: u128) -> u64 {
    if n == 0 {
        return 0;
    }
    let mut x = n;
    let mut y = (x + 1) / 2;
    while y < x {
        x = y;
        y = (x + n / x) / 2;
    }
    x as u64
}
pub fn deposit_math(a: u64, b: u64, r_a: u64, r_b: u64, supply: u64) -> Result<(u64, u64, u64)> {
    if supply == 0 {
        let initial = sqrt(u128::from(a) * u128::from(b));
        require!(initial > LOCKED_LP, AmmError::ZeroAmount);
        return Ok((a, b, initial - LOCKED_LP));
    }
    let shares = mul_div(a, supply, r_a)?.min(mul_div(b, supply, r_b)?);
    require!(shares > 0, AmmError::ZeroAmount);
    let used_a = ceil_div(shares, r_a, supply)?;
    let used_b = ceil_div(shares, r_b, supply)?;
    require!(used_a <= a && used_b <= b, AmmError::Slippage);
    Ok((used_a, used_b, shares))
}
#[event]
pub struct SwapEvent {
    pub user: Pubkey,
    pub a_to_b: bool,
    pub input: u64,
    pub output: u64,
}
#[event]
pub struct LiquidityEvent {
    pub user: Pubkey,
    pub deposit: bool,
    pub a: u64,
    pub b: u64,
    pub shares: u64,
}
#[error_code]
pub enum AmmError {
    #[msg("Mint addresses must be distinct and sorted")]
    MintOrder,
    #[msg("Amount or output is zero or too small")]
    ZeroAmount,
    #[msg("Minimum output or shares not met")]
    Slippage,
    #[msg("Checked arithmetic overflow")]
    Overflow,
    #[msg("Pool has no liquidity")]
    EmptyPool,
    #[msg("Transaction quote expired")]
    Expired,
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn initial_and_proportional_deposits() {
        assert_eq!(
            deposit_math(20_000_000, 2_000_000_000, 0, 0, 0).unwrap(),
            (20_000_000, 2_000_000_000, 199_999_000)
        );
        assert_eq!(
            deposit_math(
                2_000_000,
                500_000_000,
                20_000_000,
                2_000_000_000,
                200_000_000
            )
            .unwrap(),
            (2_000_000, 200_000_000, 20_000_000)
        );
    }
    #[test]
    fn output_and_invariant() {
        let x = 18_097_823;
        let y = 1_995_000_000;
        let input = 100_000_000;
        let out = swap_math(input, y, x).unwrap();
        assert_eq!(out, 861389);
        assert!(
            (u128::from(y) + u128::from(input)) * (u128::from(x) - u128::from(out))
                >= u128::from(x) * u128::from(y)
        );
    }
    #[test]
    fn extremes_fail_closed() {
        assert!(swap_math(0, 10, 10).is_err());
        assert!(swap_math(u64::MAX, u64::MAX, u64::MAX).is_err());
        assert!(deposit_math(1, 1, 0, 0, 0).is_err());
        assert!(deposit_math(1, 1, 10, 10, 1).is_err());
        assert_eq!(sqrt(u128::from(u64::MAX) * u128::from(u64::MAX)), u64::MAX);
    }
}
