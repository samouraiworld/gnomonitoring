	CREATE TABLE IF NOT EXISTS users (
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    	user_id TEXT PRIMARY KEY,
        nameuser TEXT NOT NULL,             -- Clerk user_id (ex: "user_abc123")
    	email TEXT NOT NULL UNIQUE,       -- email get from Clerk
    	daily_report_hour INTEGER DEFAULT 9,     -- Hour send repport
    	daily_report_minute INTEGER DEFAULT 0    -- minute send report 
	);
    	CREATE TABLE IF NOT EXISTS alert_contacts (
        created_at DEFAULT CURRENT_TIMESTAMP,
    	id INTEGER PRIMARY KEY AUTOINCREMENT,
    	user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
		moniker TEXT NOT NULL,
    	namecontact TEXT NOT NULL,
    	mention_tag TEXT         -- ex: @bob, <@1234567890>
    );
	
	CREATE TABLE IF NOT EXISTS webhooks_govdao (
        created_at DEFAULT CURRENT_TIMESTAMP,
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		description TEXT,
		user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
		url TEXT NOT NULL,
		type TEXT NOT NULL CHECK(type IN ('discord', 'slack')),
		last_checked_id INTEGER NOT NULL DEFAULT 0
	);

	
	CREATE TABLE IF NOT EXISTS webhooks_validator (
        created_at DEFAULT CURRENT_TIMESTAMP,
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		description TEXT,
    	user_id TEXT  NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    	url TEXT NOT NULL,
    	type TEXT NOT NULL CHECK(type IN ('discord', 'slack'))
	);
	
	CREATE TABLE IF NOT EXISTS daily_participation (
        date TEXT NOT NULL,
        block_height INTEGER NOT NULL,
        moniker TEXT NOT NULL,
        addr TEXT NOT NULL,
        participated BOOLEAN NOT NULL,
        PRIMARY KEY (date, block_height, moniker)
	);
	CREATE TABLE IF NOT EXISTS alert_log (
    user_id TEXT NOT NULL,
    addr TEXT NOT NULL,
    moniker TEXT NOT NULL,
    level TEXT NOT NULL,
	url TEXT,
    sent_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, addr, level,url)
);
	
	CREATE INDEX IF NOT EXISTS idx_participation_date ON daily_participation(date);
    CREATE INDEX IF NOT EXISTS idx_webhooks_validator_user ON webhooks_validator(user_id);
    CREATE INDEX IF NOT EXISTS idx_webhooks_govdao_user ON webhooks_govdao(user_id);



	/*UPDATE daily_participation
SET participated = 0
WHERE rowid IN (
    SELECT rowid
    FROM daily_participation
    WHERE moniker = 'samourai-dev-team-1'
    ORDER BY date DESC, block_height DESC
    LIMIT 3
);