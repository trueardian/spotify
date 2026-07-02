CREATE TABLE IF NOT EXISTS "spotify_tokens" (
    "owner"         text                     PRIMARY KEY,
    "refresh_token" text,
    "created_at"    timestamp with time zone DEFAULT NOW()
);
