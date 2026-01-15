package timescale

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"hl-carry-bot/internal/config"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

const writeTimeout = 3 * time.Second

type Candle struct {
	Asset    string
	Interval string
	Start    time.Time
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
}

type PositionSnapshot struct {
	Time            time.Time
	State           string
	SpotAsset       string
	PerpAsset       string
	SpotBalance     float64
	PerpPosition    float64
	SpotMid         float64
	PerpMid         float64
	OraclePrice     float64
	FundingRate     float64
	Volatility      float64
	DeltaUSD        float64
	SpotExposureUSD float64
	PerpExposureUSD float64
	NotionalUSD     float64
	MarginRatio     float64
	HealthRatio     float64
	HasMarginRatio  bool
	HasHealthRatio  bool
	OpenOrders      int
}

type Writer struct {
	db         *sql.DB
	log        *zap.Logger
	schema     string
	positions  chan PositionSnapshot
	candles    chan Candle
	started    atomic.Bool
	dropPos    atomic.Uint64
	dropCandle atomic.Uint64
}

func New(cfg config.TimescaleConfig, log *zap.Logger) (*Writer, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		return nil, errors.New("timescale dsn is required")
	}
	schema := strings.TrimSpace(cfg.Schema)
	if schema == "" {
		schema = "public"
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = 256
	}
	writer := &Writer{
		db:        db,
		log:       log,
		schema:    schema,
		positions: make(chan PositionSnapshot, queueSize),
		candles:   make(chan Candle, queueSize),
	}
	if err := writer.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return writer, nil
}

func (w *Writer) Start(ctx context.Context) {
	if w == nil {
		return
	}
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	go w.run(ctx)
}

func (w *Writer) Close() error {
	if w == nil || w.db == nil {
		return nil
	}
	return w.db.Close()
}

func (w *Writer) EnqueuePosition(snapshot PositionSnapshot) {
	if w == nil {
		return
	}
	select {
	case w.positions <- snapshot:
		return
	default:
		if w.dropPos.Add(1) == 1 && w.log != nil {
			w.log.Warn("timescale position queue full")
		}
	}
}

func (w *Writer) EnqueueCandle(candle Candle) {
	if w == nil {
		return
	}
	select {
	case w.candles <- candle:
		return
	default:
		if w.dropCandle.Add(1) == 1 && w.log != nil {
			w.log.Warn("timescale candle queue full")
		}
	}
}

func (w *Writer) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case snap := <-w.positions:
			w.writePosition(ctx, snap)
		case candle := <-w.candles:
			w.writeCandle(ctx, candle)
		}
	}
}

func (w *Writer) ensureSchema(ctx context.Context) error {
	if w.db == nil {
		return errors.New("timescale db not initialized")
	}
	if w.schema != "public" {
		if err := w.exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", w.schema)); err != nil {
			return err
		}
	}
	if err := w.exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		ts TIMESTAMPTZ NOT NULL,
		asset TEXT NOT NULL,
		interval TEXT NOT NULL,
		open DOUBLE PRECISION NOT NULL,
		high DOUBLE PRECISION NOT NULL,
		low DOUBLE PRECISION NOT NULL,
		close DOUBLE PRECISION NOT NULL,
		volume DOUBLE PRECISION NOT NULL DEFAULT 0,
		PRIMARY KEY (ts, asset, interval)
	)`, w.table("market_ohlc"))); err != nil {
		return err
	}
	if err := w.exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		ts TIMESTAMPTZ NOT NULL,
		state TEXT NOT NULL,
		spot_asset TEXT NOT NULL,
		perp_asset TEXT NOT NULL,
		spot_balance DOUBLE PRECISION NOT NULL,
		perp_position DOUBLE PRECISION NOT NULL,
		spot_mid DOUBLE PRECISION NOT NULL,
		perp_mid DOUBLE PRECISION NOT NULL,
		oracle_price DOUBLE PRECISION NOT NULL,
		funding_rate DOUBLE PRECISION NOT NULL,
		volatility DOUBLE PRECISION NOT NULL,
		delta_usd DOUBLE PRECISION NOT NULL,
		spot_exposure_usd DOUBLE PRECISION NOT NULL,
		perp_exposure_usd DOUBLE PRECISION NOT NULL,
		notional_usd DOUBLE PRECISION NOT NULL,
		margin_ratio DOUBLE PRECISION NOT NULL,
		health_ratio DOUBLE PRECISION NOT NULL,
		has_margin_ratio BOOLEAN NOT NULL,
		has_health_ratio BOOLEAN NOT NULL,
		open_orders INTEGER NOT NULL
	)`, w.table("position_snapshots"))); err != nil {
		return err
	}
	if err := w.exec(ctx, "CREATE EXTENSION IF NOT EXISTS timescaledb"); err != nil {
		if w.log != nil {
			w.log.Warn("timescale extension ensure failed", zap.Error(err))
		}
		return nil
	}
	if err := w.exec(ctx, fmt.Sprintf("SELECT create_hypertable('%s', 'ts', if_not_exists => TRUE)", w.table("market_ohlc"))); err != nil && w.log != nil {
		w.log.Warn("timescale market_ohlc hypertable create failed", zap.Error(err))
	}
	if err := w.exec(ctx, fmt.Sprintf("SELECT create_hypertable('%s', 'ts', if_not_exists => TRUE)", w.table("position_snapshots"))); err != nil && w.log != nil {
		w.log.Warn("timescale position_snapshots hypertable create failed", zap.Error(err))
	}
	return nil
}

func (w *Writer) writePosition(ctx context.Context, snap PositionSnapshot) {
	if w.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	query := fmt.Sprintf(`INSERT INTO %s (
		ts, state, spot_asset, perp_asset, spot_balance, perp_position, spot_mid, perp_mid,
		oracle_price, funding_rate, volatility, delta_usd, spot_exposure_usd, perp_exposure_usd,
		notional_usd, margin_ratio, health_ratio, has_margin_ratio, has_health_ratio, open_orders
	) VALUES (
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20
	)`, w.table("position_snapshots"))
	if _, err := w.db.ExecContext(ctx, query,
		snap.Time,
		snap.State,
		snap.SpotAsset,
		snap.PerpAsset,
		snap.SpotBalance,
		snap.PerpPosition,
		snap.SpotMid,
		snap.PerpMid,
		snap.OraclePrice,
		snap.FundingRate,
		snap.Volatility,
		snap.DeltaUSD,
		snap.SpotExposureUSD,
		snap.PerpExposureUSD,
		snap.NotionalUSD,
		snap.MarginRatio,
		snap.HealthRatio,
		snap.HasMarginRatio,
		snap.HasHealthRatio,
		snap.OpenOrders,
	); err != nil && w.log != nil {
		w.log.Warn("timescale position insert failed", zap.Error(err))
	}
}

func (w *Writer) writeCandle(ctx context.Context, candle Candle) {
	if w.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	query := fmt.Sprintf(`INSERT INTO %s (
		ts, asset, interval, open, high, low, close, volume
	) VALUES (
		$1,$2,$3,$4,$5,$6,$7,$8
	)
	ON CONFLICT (ts, asset, interval) DO UPDATE SET
		open = EXCLUDED.open,
		high = EXCLUDED.high,
		low = EXCLUDED.low,
		close = EXCLUDED.close,
		volume = EXCLUDED.volume`, w.table("market_ohlc"))
	if _, err := w.db.ExecContext(ctx, query,
		candle.Start,
		candle.Asset,
		candle.Interval,
		candle.Open,
		candle.High,
		candle.Low,
		candle.Close,
		candle.Volume,
	); err != nil && w.log != nil {
		w.log.Warn("timescale candle upsert failed", zap.Error(err))
	}
}

func (w *Writer) exec(ctx context.Context, query string) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_, err := w.db.ExecContext(ctx, query)
	return err
}

func (w *Writer) table(name string) string {
	return w.schema + "." + name
}
