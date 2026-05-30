mod host;
mod message;
pub mod oauth_pkce;
pub mod ringbuf;
mod sidecar_commands;
mod sidecar_supervisor;
pub mod twitch_auth;
pub mod youtube_auth;

pub mod emote_index;

// Re-exports for the bench harness. Gated so the public crate surface
// does not grow with bench-only plumbing in release builds.
#[cfg(any(test, feature = "__bench"))]
#[doc(hidden)]
pub use host::parse_batch;
#[cfg(any(test, feature = "__bench"))]
#[doc(hidden)]
pub use message::UnifiedMessage;

use tauri::{Manager, Runtime};
use tracing_subscriber::EnvFilter;

#[tauri::command]
fn get_platform() -> &'static str {
    std::env::consts::OS
}

pub fn run() {
    // Phase 0 dev convenience: in debug builds only, load `.env.local` from
    // cwd or any parent before anything reads PRISMOID_TWITCH_*. Release
    // builds skip this entirely so a stray file in the install dir cannot
    // change runtime behavior. Real secret storage uses OS keychain via
    // OAuth, see docs/platform-apis.md §Twitch.
    #[cfg(debug_assertions)]
    match dotenvy::from_filename(".env.local") {
        Ok(_) => {}
        Err(dotenvy::Error::Io(err)) if err.kind() == std::io::ErrorKind::NotFound => {}
        Err(err) => eprintln!("failed to load .env.local: {err}"),
    }

    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::from_default_env().add_directive("prismoid=debug".parse().unwrap()),
        )
        .init();

    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_shell::init())
        .invoke_handler(tauri::generate_handler![
            get_platform,
            twitch_auth::commands::twitch_auth_status,
            twitch_auth::commands::twitch_start_login,
            twitch_auth::commands::twitch_complete_login,
            twitch_auth::commands::twitch_cancel_login,
            twitch_auth::commands::twitch_logout,
            youtube_auth::commands::youtube_auth_status,
            youtube_auth::commands::youtube_start_login,
            youtube_auth::commands::youtube_complete_login,
            youtube_auth::commands::youtube_cancel_login,
            youtube_auth::commands::youtube_logout,
            sidecar_commands::twitch_send_message,
            sidecar_commands::twitch_moderation_action,
            sidecar_commands::youtube_send_message,
        ])
        .setup(setup)
        .run(tauri::generate_context!())
        .expect("failed to run prismoid");
}

/// Tauri setup hook. Builds the shared `AuthManager` + wakeup notifier,
/// registers them as managed state for the auth UI commands, and (on
/// Windows) hands clones to the sidecar supervisor so a successful
/// sign-in wakes it from `waiting_for_auth` immediately. Non-Windows
/// targets log a warning and let the frontend boot without the sidecar
/// (ADR 18).
#[allow(clippy::unnecessary_wraps)]
fn setup<R: Runtime>(app: &mut tauri::App<R>) -> Result<(), Box<dyn std::error::Error>> {
    use std::sync::Arc;
    use tokio::sync::Notify;
    use twitch_auth::{AuthManager, AuthState, KeychainStore, TWITCH_CLIENT_ID};
    use twitch_oauth2::Scope;

    let http_client = match reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .build()
    {
        Ok(client) => client,
        Err(err) => {
            tracing::error!(
                error = %err,
                "failed to build reqwest client; skipping auth manager and sidecar"
            );
            return Ok(());
        }
    };
    let auth = Arc::new(
        AuthManager::builder(TWITCH_CLIENT_ID)
            .scope(Scope::UserReadChat)
            .scope(Scope::UserWriteChat)
            .scope(Scope::ModeratorManageBannedUsers)
            .scope(Scope::ModeratorManageChatMessages)
            .build(KeychainStore, http_client),
    );
    let wakeup = Arc::new(Notify::new());
    app.manage(AuthState::new(auth.clone(), wakeup.clone()));

    // YouTube auth manager + state. Sidecar wiring lands separately —
    // this PR only stands up the OAuth + keychain stack and the
    // frontend sign-in surface; the supervisor still only knows
    // about Twitch tokens. ADR 39.
    {
        use youtube_auth::{
            AuthManager as YtAuthManager, AuthState as YtAuthState,
            KeychainStore as YtKeychainStore, GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET,
            SCOPE_YOUTUBE, SCOPE_YOUTUBE_READONLY,
        };
        match reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .build()
        {
            Ok(yt_http_client) => {
                let yt_auth = Arc::new(
                    YtAuthManager::builder(GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET)
                        .scope(SCOPE_YOUTUBE_READONLY)
                        .scope(SCOPE_YOUTUBE)
                        .build(YtKeychainStore, yt_http_client),
                );
                let yt_wakeup = Arc::new(Notify::new());
                app.manage(YtAuthState::new(yt_auth, yt_wakeup));
            }
            Err(err) => {
                tracing::error!(
                    error = %err,
                    "failed to build reqwest client for YouTube; skipping YouTube auth wiring"
                );
            }
        }
    }

    let sender = sidecar_commands::SidecarCommandSender::default();
    app.manage(sender.clone());

    #[cfg(windows)]
    {
        sidecar_supervisor::spawn(app.app_handle().clone(), auth, wakeup, sender);
    }
    #[cfg(not(windows))]
    {
        let _ = (auth, wakeup, sender);
        tracing::warn!(
            "sidecar lifecycle is Windows-only for now; launching frontend without sidecar"
        );
    }
    Ok(())
}
