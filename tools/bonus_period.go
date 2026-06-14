package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	appie "github.com/gwillem/appie-go"
)

// This file adds support for fetching the bonus offers of a *future* bonus
// period (notably "next week", https://www.ah.nl/bonus/volgende-week) in
// addition to the current week.
//
// The upstream appie-go client only exposes GetBonusProducts /
// GetSpotlightBonusProducts, both of which are hard-coded to time.Now() and
// therefore always return the *current* week's bonus. The AH bonus metadata
// endpoint, however, already returns multiple periods (the current week is
// periods[0], next week is periods[1] once AH has published it). The section
// endpoint accepts an explicit `date` parameter, so by selecting a date inside
// a later period we can retrieve that period's offers using the exact same
// API surface.
//
// Because productResponse and the metadata/section structs are unexported in
// appie-go, the relevant shapes are mirrored here and consumed through the
// exported Client.DoRequest primitive.

// bonusPeriod identifies which weekly bonus period to fetch.
type bonusPeriod int

const (
	bonusPeriodCurrent bonusPeriod = 0
	bonusPeriodNext    bonusPeriod = 1
)

type bonusMetadataResponse struct {
	Periods []struct {
		BonusStartDate string `json:"bonusStartDate"`
		BonusEndDate   string `json:"bonusEndDate"`
		Tabs           []struct {
			Description     string `json:"description"`
			URLMetadataList []struct {
				URL         string `json:"url"`
				Count       int    `json:"count"`
				BonusType   string `json:"bonusType"`
				Description string `json:"description"`
			} `json:"urlMetadataList"`
		} `json:"tabs"`
	} `json:"periods"`
}

type bonusSectionResponse struct {
	BonusGroupOrProducts []struct {
		Product    *bonusSectionProduct `json:"product,omitempty"`
		BonusGroup *bonusSectionGroup   `json:"bonusGroup,omitempty"`
	} `json:"bonusGroupOrProducts"`
}

type bonusSectionProduct struct {
	WebshopID        int           `json:"webshopId"`
	Title            string        `json:"title"`
	Brand            string        `json:"brand"`
	SalesUnitSize    string        `json:"salesUnitSize"`
	Images           []appie.Image `json:"images"`
	CurrentPrice     float64       `json:"currentPrice"`
	PriceBeforeBonus float64       `json:"priceBeforeBonus"`
	IsBonus          bool          `json:"isBonus"`
	BonusMechanism   string        `json:"bonusMechanism"`
	MainCategory     string        `json:"mainCategory"`
	SubCategory      string        `json:"subCategory"`
}

func (p *bonusSectionProduct) toProduct() appie.Product {
	price := p.CurrentPrice
	if price == 0 {
		price = p.PriceBeforeBonus
	}
	return appie.Product{
		ID:             p.WebshopID,
		WebshopID:      strconv.Itoa(p.WebshopID),
		Title:          p.Title,
		Brand:          p.Brand,
		Category:       p.MainCategory,
		SubCategory:    p.SubCategory,
		Price:          appie.Price{Now: price, Was: p.PriceBeforeBonus},
		Images:         p.Images,
		IsBonus:        p.IsBonus,
		BonusMechanism: p.BonusMechanism,
		UnitSize:       p.SalesUnitSize,
	}
}

type bonusSectionGroup struct {
	ID                  string                `json:"id"`
	SegmentDescription  string                `json:"segmentDescription"`
	DiscountDescription string                `json:"discountDescription"`
	Category            string                `json:"category"`
	Images              []appie.Image         `json:"images"`
	Products            []bonusSectionProduct `json:"products"`
	ExampleFromPrice    float64               `json:"exampleFromPrice"`
	ExampleForPrice     float64               `json:"exampleForPrice"`
}

func (g *bonusSectionGroup) toProduct() appie.Product {
	return appie.Product{
		Title:          g.SegmentDescription,
		Category:       g.Category,
		BonusMechanism: g.DiscountDescription,
		IsBonus:        true,
		BonusSegmentID: g.ID,
		Price:          appie.Price{Now: g.ExampleForPrice, Was: g.ExampleFromPrice},
		Images:         g.Images,
	}
}

// getBonusProductsForPeriod returns the bonus products for the requested weekly
// period (current or next). It mirrors appie-go's GetBonusProducts flow but
// pins the section `date` to the selected period so future weeks resolve
// correctly. The boolean return reports whether the requested period exists yet
// (AH publishes next week's bonus a few days in advance, so it may be absent).
func getBonusProductsForPeriod(ctx context.Context, c *appie.Client, period bonusPeriod) ([]appie.Product, bool, error) {
	var meta bonusMetadataResponse
	if err := c.DoRequest(ctx, http.MethodGet, "/mobile-services/bonuspage/v3/metadata", nil, &meta); err != nil {
		return nil, false, fmt.Errorf("get bonus metadata failed: %w", err)
	}
	if int(period) >= len(meta.Periods) {
		return nil, false, nil
	}
	p := meta.Periods[period]

	// Pick a date inside the period to drive the section endpoint. The start
	// date is always within the period; fall back to today only if absent.
	date := p.BonusStartDate
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	// Collect the national category names for this period.
	var categories []string
	seenCat := make(map[string]bool)
	for _, tab := range p.Tabs {
		for _, m := range tab.URLMetadataList {
			if m.BonusType == "NATIONAL" && m.Description != "" && !seenCat[m.Description] {
				seenCat[m.Description] = true
				categories = append(categories, m.Description)
			}
		}
	}

	seen := make(map[string]bool)
	var products []appie.Product
	for _, category := range categories {
		catProducts, err := getBonusSectionForDate(ctx, c, category, date)
		if err != nil {
			return nil, true, err
		}
		for _, prod := range catProducts {
			key := fmt.Sprintf("%d:%s", prod.ID, prod.Title)
			if !seen[key] {
				seen[key] = true
				products = append(products, prod)
			}
		}
	}
	return products, true, nil
}

func getBonusSectionForDate(ctx context.Context, c *appie.Client, category, date string) ([]appie.Product, error) {
	params := url.Values{}
	params.Set("application", "AHWEBSHOP")
	params.Set("date", date)
	params.Set("promotionType", "NATIONAL")
	params.Set("category", category)

	path := "/mobile-services/bonuspage/v2/section?" + params.Encode()

	var result bonusSectionResponse
	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("get bonus products failed (category=%s): %w", category, err)
	}

	var products []appie.Product
	for _, item := range result.BonusGroupOrProducts {
		if item.Product != nil {
			products = append(products, item.Product.toProduct())
		}
		if item.BonusGroup != nil {
			if len(item.BonusGroup.Products) > 0 {
				for i := range item.BonusGroup.Products {
					products = append(products, item.BonusGroup.Products[i].toProduct())
				}
			} else {
				products = append(products, item.BonusGroup.toProduct())
			}
		}
	}
	return products, nil
}
