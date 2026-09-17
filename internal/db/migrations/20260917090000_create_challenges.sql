-- +goose Up
CREATE TABLE challenges (
    id             UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    slug           VARCHAR(100) NOT NULL UNIQUE,
    title          VARCHAR(200) NOT NULL,
    description    TEXT         NOT NULL,
    category       VARCHAR(32)  NOT NULL,
    difficulty     VARCHAR(16)  NOT NULL,
    constraints    JSONB        NOT NULL DEFAULT '{}'::jsonb,
    judge_criteria JSONB        NOT NULL,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT challenges_category_check
        CHECK (category IN ('creative_writing', 'coding', 'data_extraction', 'summarization', 'reasoning')),
    CONSTRAINT challenges_difficulty_check
        CHECK (difficulty IN ('easy', 'medium', 'hard')),
    CONSTRAINT challenges_constraints_object_check
        CHECK (jsonb_typeof(constraints) = 'object'),
    CONSTRAINT challenges_judge_criteria_object_check
        CHECK (jsonb_typeof(judge_criteria) = 'object')
);

CREATE INDEX challenges_category_idx ON challenges (category);
CREATE INDEX challenges_difficulty_idx ON challenges (difficulty);

-- +goose Down
DROP TABLE IF EXISTS challenges;
