# Found Footy - Simplified Deployment Guide

## 🚀 Two Simple Deployments

### 1. `fixtures-flow-daily` - Scheduled Runner
- **Purpose**: Automatic daily monitoring
- **Schedule**: Daily at 00:05 UTC (DISABLED by default)
- **Configuration**: Via Prefect UI parameters

**To use:**
1. Go to Prefect UI → Deployments → `fixtures-flow-daily`
2. Edit parameters → Change `league_ids` for your competitions
3. Enable the schedule when ready

### 2. `fixtures-flow-manual` - Instant Runner  
- **Purpose**: Run immediately for specific date/leagues
- **Trigger**: Manual only
- **Configuration**: Set parameters each time you run

**To use:**
1. Go to Prefect UI → Deployments → `fixtures-flow-manual`
2. Click "Run" → Set parameters → Start

## 🏆 League ID Quick Reference

| Competition | League ID | Parameter Example |
|-------------|-----------|-------------------|
| **English** | | |
| Premier League | 39 | `"[39]"` |
| FA Cup | 48 | `"[48]"` |
| EFL Cup | 45 | `"[45]"` |
| All English | | `"[39,48,45]"` |
| **Spanish** | | |
| La Liga | 140 | `"[140]"` |
| Copa del Rey | 143 | `"[143]"` |
| Supercopa | 556 | `"[556]"` |
| All Spanish | | `"[140,143,556]"` |
| **European** | | |
| Champions League | 2 | `"[2]"` |
| Europa League | 3 | `"[3]"` |
| Conference League | 848 | `"[848]"` |
| All European | | `"[2,3,848]"` |
| **Major Domestic** | | |
| Top 5 Leagues | | `"[39,140,78,61,135]"` |

## 📅 Date Format Examples

- `null` or empty = Today's matches
- `"20250828"` = Specific date (August 28, 2025)
- `"20250215"` = February 15, 2025

## 🎯 Common Use Cases

### Daily Premier League Monitoring
**Deployment**: `fixtures-flow-daily`
**Parameters**: 
```json
{
  "date_str": null,
  "league_ids": "[39]"
}
```
**Enable schedule in UI**

### Champions League Match Day
**Deployment**: `fixtures-flow-manual`  
**Parameters**:
```json
{
  "date_str": "20250212",
  "league_ids": "[2]"
}
```
**Run manually**

### Weekend Multi-League
**Deployment**: `fixtures-flow-manual`
**Parameters**:
```json
{
  "date_str": "20250215", 
  "league_ids": "[39,140,78,61,135]"
}
```
**Run manually**

### Cup Final Day
**Deployment**: `fixtures-flow-manual`
**Parameters**:
```json
{
  "date_str": "20250517",
  "league_ids": "[48,143,81]"
}
```
**Run manually**