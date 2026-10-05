package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterOrderTools registers order-related MCP tools.
func RegisterOrderTools(s *server.MCPServer, deps Deps) {
	registerGetOrderHistory(s, deps)
	registerGetPastOrders(s, deps)
	registerGetOrderDetails(s, deps)
	registerGetFrequentItems(s, deps)
	registerGetReceipts(s, deps)
	registerGetReceiptDetails(s, deps)
	registerGetCart(s, deps)
	registerGetCartSummary(s, deps)
	registerUpdateCartItem(s, deps)
	registerRemoveFromCart(s, deps)
	registerClearCart(s, deps)
	registerReopenOrder(s, deps)
	registerUpdateOrderItems(s, deps)
	registerRevertOrder(s, deps)
}

// --- ah_get_order_history ---

func registerGetOrderHistory(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_order_history",
		mcp.WithTitleAnnotation("Albert Heijn: Order History"),
		mcp.WithDescription(
			"Get upcoming Albert Heijn online delivery orders (open fulfillments). "+
				"Returns id, date, total_price, status, modifiable flag. "+
				"Use the returned id with ah_reopen_order to edit a submitted order before its closing time.",
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of orders to return (default 10)"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		limit := req.GetInt("limit", 10)

		fulfillments, err := c.GetFulfillments(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get order history: %v", err)), nil
		}

		type orderEntry struct {
			ID          int     `json:"id"`
			Date        string  `json:"date,omitempty"`
			TimeWindow  string  `json:"time_window,omitempty"`
			TotalPrice  float64 `json:"total_price"`
			ItemCount   int     `json:"item_count,omitempty"`
			Status      string  `json:"status"`
			ShoppingType string `json:"shopping_type,omitempty"`
			Modifiable  bool    `json:"modifiable"`
		}
		results := make([]orderEntry, 0, len(fulfillments))
		for i, f := range fulfillments {
			if i >= limit {
				break
			}
			date := f.Delivery.Slot.DateDisplay
			if date == "" {
				date = f.Delivery.Slot.Date
			}
			results = append(results, orderEntry{
				ID:          f.OrderID,
				Date:        date,
				TimeWindow:  f.Delivery.Slot.TimeDisplay,
				TotalPrice:  f.TotalPrice,
				Status:      f.StatusDescription,
				ShoppingType: f.ShoppingType,
				Modifiable:  f.Modifiable,
			})
		}
		return jsonResult(results)
	})
}

// --- ah_get_past_orders ---

func registerGetPastOrders(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_past_orders",
		mcp.WithTitleAnnotation("Albert Heijn: Past Orders"),
		mcp.WithDescription(
			"Get past/delivered Albert Heijn online delivery orders. "+
				"Returns id, date, total_price, status. "+
				"Use the returned id with ah_get_order_details to see full item lists.",
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of orders to return (default 10)"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		limit := req.GetInt("limit", 10)

		const query = `query OrderFulfillmentsClosed {
  orderFulfillments(status: CLOSED) {
    result {
      orderId
      statusCode
      statusDescription
      shoppingType
      transactionCompleted
      totalPrice {
        totalPrice { amount }
      }
      delivery {
        status
        slot {
          date
          dateDisplay
          timeDisplay
        }
      }
    }
  }
}`
		type fulfillmentResult struct {
			OrderID           int    `json:"orderId"`
			StatusDescription string `json:"statusDescription"`
			ShoppingType      string `json:"shoppingType"`
			TotalPrice        struct {
				TotalPrice struct {
					Amount float64 `json:"amount"`
				} `json:"totalPrice"`
			} `json:"totalPrice"`
			Delivery struct {
				Status string `json:"status"`
				Slot   struct {
					Date        string `json:"date"`
					DateDisplay string `json:"dateDisplay"`
					TimeDisplay string `json:"timeDisplay"`
				} `json:"slot"`
			} `json:"delivery"`
		}
		type response struct {
			OrderFulfillments struct {
				Result []fulfillmentResult `json:"result"`
			} `json:"orderFulfillments"`
		}
		var resp response
		if err := c.DoGraphQL(ctx, query, nil, &resp); err != nil {
			return errResult(fmt.Sprintf("Failed to get past orders: %v", err)), nil
		}

		type orderEntry struct {
			ID           int     `json:"id"`
			Date         string  `json:"date,omitempty"`
			TimeWindow   string  `json:"time_window,omitempty"`
			TotalPrice   float64 `json:"total_price"`
			Status       string  `json:"status"`
			ShoppingType string  `json:"shopping_type,omitempty"`
		}
		results := make([]orderEntry, 0)
		for i, f := range resp.OrderFulfillments.Result {
			if i >= limit {
				break
			}
			date := f.Delivery.Slot.DateDisplay
			if date == "" {
				date = f.Delivery.Slot.Date
			}
			results = append(results, orderEntry{
				ID:           f.OrderID,
				Date:         date,
				TimeWindow:   f.Delivery.Slot.TimeDisplay,
				TotalPrice:   f.TotalPrice.TotalPrice.Amount,
				Status:       f.StatusDescription,
				ShoppingType: f.ShoppingType,
			})
		}
		if len(results) == 0 {
			return mcp.NewToolResultText("No past orders found."), nil
		}
		return jsonResult(results)
	})
}

// --- ah_get_frequent_items ---

func registerGetFrequentItems(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_frequent_items",
		mcp.WithTitleAnnotation("Albert Heijn: Frequently Ordered Items"),
		mcp.WithDescription(
			"Get frequently ordered products by analysing order history. "+
				"Fetches all fulfillments, expands each order's items, counts per product, "+
				"and returns products ordered at least min_order_count times. "+
				"Returns product_name, product_id, order_count, last_ordered_date.",
		),
		mcp.WithString("min_order_count",
			mcp.Description("Minimum number of orders a product must appear in (default 3)"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		minCount := req.GetInt("min_order_count", 3)

		// Fetch both open and past (CLOSED) fulfillments so we have a full history.
		openFulfillments, err := c.GetFulfillments(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get open fulfillments: %v", err)), nil
		}

		const closedQuery = `query OrderFulfillmentsClosed {
  orderFulfillments(status: CLOSED) {
    result {
      orderId
      delivery { slot { date } }
    }
  }
}`
		type closedResult struct {
			OrderID  int `json:"orderId"`
			Delivery struct {
				Slot struct{ Date string `json:"date"` } `json:"slot"`
			} `json:"delivery"`
		}
		type closedResp struct {
			OrderFulfillments struct {
				Result []closedResult `json:"result"`
			} `json:"orderFulfillments"`
		}
		var cr closedResp
		_ = c.DoGraphQL(ctx, closedQuery, nil, &cr) // ignore error — CLOSED may not exist

		type minFulfillment struct {
			OrderID   int
			OrderDate string
		}
		var allFulfillments []minFulfillment
		for _, f := range openFulfillments {
			allFulfillments = append(allFulfillments, minFulfillment{OrderID: f.OrderID, OrderDate: f.Delivery.Slot.Date})
		}
		for _, f := range cr.OrderFulfillments.Result {
			allFulfillments = append(allFulfillments, minFulfillment{OrderID: f.OrderID, OrderDate: f.Delivery.Slot.Date})
		}

		type productStats struct {
			Name          string
			Count         int
			LastOrderDate string
		}
		stats := map[int]*productStats{}

		for _, f := range allFulfillments {
			orderDate := f.OrderDate

			order, oErr := c.GetOrderDetails(ctx, f.OrderID)
			if oErr != nil {
				fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Warning: could not fetch order %d: %v\n", f.OrderID, oErr)
				continue
			}
			seen := map[int]bool{}
			for _, item := range order.Items {
				pid := item.ProductID
				if seen[pid] {
					continue
				}
				seen[pid] = true

				name := ""
				if item.Product != nil {
					name = item.Product.Title
				}
				if name == "" {
					name = strconv.Itoa(pid)
				}

				if stats[pid] == nil {
					stats[pid] = &productStats{Name: name}
				}
				stats[pid].Count++
				// Keep latest date
				if orderDate > stats[pid].LastOrderDate {
					stats[pid].LastOrderDate = orderDate
					stats[pid].Name = name // update name in case it was empty before
				}
			}
		}

		type item struct {
			ProductName     string `json:"product_name"`
			ProductID       int    `json:"product_id"`
			OrderCount      int    `json:"order_count"`
			LastOrderedDate string `json:"last_ordered_date,omitempty"`
		}
		results := []item{}
		for pid, s := range stats {
			if s.Count >= minCount {
				lastDate := s.LastOrderDate
				if t, err := time.Parse("2006-01-02", lastDate); err == nil {
					lastDate = t.Format("2006-01-02")
				}
				results = append(results, item{
					ProductName:     s.Name,
					ProductID:       pid,
					OrderCount:      s.Count,
					LastOrderedDate: lastDate,
				})
			}
		}
		sort.Slice(results, func(i, j int) bool {
			return results[i].OrderCount > results[j].OrderCount
		})
		return jsonResult(results)
	})
}

// --- ah_get_receipts ---

func registerGetReceipts(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_receipts",
		mcp.WithTitleAnnotation("Albert Heijn: Receipts"),
		mcp.WithDescription(
			"List recent Albert Heijn in-store receipts (kassabonnen). "+
				"Returns receipt id, date, and total amount. "+
				"Use ah_get_receipt_details with the id to see individual items.",
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of receipts to return (default 10)"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		limit := req.GetInt("limit", 10)

		receipts, err := c.GetReceipts(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get receipts: %v", err)), nil
		}

		type entry struct {
			ID          string  `json:"id"`
			Date        string  `json:"date"`
			TotalAmount float64 `json:"total_amount"`
		}
		results := make([]entry, 0, len(receipts))
		for i, r := range receipts {
			if i >= limit {
				break
			}
			// Reformat ISO datetime to readable date
			date := r.Date
			if t, err := time.Parse(time.RFC3339, date); err == nil {
				date = t.Format("2006-01-02 15:04")
			}
			results = append(results, entry{
				ID:          r.TransactionID,
				Date:        date,
				TotalAmount: r.TotalAmount,
			})
		}
		return jsonResult(results)
	})
}

// --- ah_get_receipt_details ---

func registerGetReceiptDetails(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_receipt_details",
		mcp.WithTitleAnnotation("Albert Heijn: Receipt Details"),
		mcp.WithDescription(
			"Get full details of a single Albert Heijn in-store receipt (kassabon) by its id. "+
				"Returns all purchased items with name, quantity, unit price and line total, "+
				"plus any discounts and payment method. "+
				"Get the id from ah_get_receipts first.",
		),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Receipt transaction ID from ah_get_receipts"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		id := req.GetString("id", "")
		if id == "" {
			return errResult("id is required"), nil
		}

		receipt, err := c.GetReceipt(ctx, id)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get receipt %s: %v", id, err)), nil
		}

		type itemEntry struct {
			Name      string  `json:"name"`
			Quantity  int     `json:"quantity,omitempty"`
			UnitPrice float64 `json:"unit_price,omitempty"`
			Total     float64 `json:"total"`
		}
		type discountEntry struct {
			Name   string  `json:"name"`
			Amount float64 `json:"amount"`
		}
		type paymentEntry struct {
			Method string  `json:"method"`
			Amount float64 `json:"amount"`
		}
		type result struct {
			ID        string          `json:"id"`
			Items     []itemEntry     `json:"items"`
			Discounts []discountEntry `json:"discounts,omitempty"`
			Payments  []paymentEntry  `json:"payments,omitempty"`
		}

		items := make([]itemEntry, 0, len(receipt.Items))
		for _, it := range receipt.Items {
			items = append(items, itemEntry{
				Name:      it.Description,
				Quantity:  it.Quantity,
				UnitPrice: it.UnitPrice,
				Total:     it.Amount,
			})
		}
		discounts := make([]discountEntry, 0, len(receipt.Discounts))
		for _, d := range receipt.Discounts {
			discounts = append(discounts, discountEntry{Name: d.Name, Amount: d.Amount})
		}
		payments := make([]paymentEntry, 0, len(receipt.Payments))
		for _, p := range receipt.Payments {
			payments = append(payments, paymentEntry{Method: p.Method, Amount: p.Amount})
		}

		return jsonResult(result{
			ID:        receipt.TransactionID,
			Items:     items,
			Discounts: discounts,
			Payments:  payments,
		})
	})
}

// --- ah_get_order_details ---

func registerGetOrderDetails(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_order_details",
		mcp.WithTitleAnnotation("Albert Heijn: Order Details"),
		mcp.WithDescription(
			"Get the full item list for a specific Albert Heijn delivery order by its ID. "+
				"Returns all products with names, quantities, and prices. "+
				"Get order_id from ah_get_order_history.",
		),
		mcp.WithString("order_id",
			mcp.Required(),
			mcp.Description("Numeric order ID from ah_get_order_history"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		orderID := req.GetInt("order_id", 0)
		if orderID == 0 {
			return errResult("order_id is required"), nil
		}

		order, err := c.GetOrderDetails(ctx, orderID)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get order details for %d: %v", orderID, err)), nil
		}

		type itemEntry struct {
			ProductID int     `json:"product_id"`
			Name      string  `json:"name,omitempty"`
			Quantity  int     `json:"quantity"`
			Price     float64 `json:"price,omitempty"`
		}
		type orderResult struct {
			ID            string      `json:"id"`
			State         string      `json:"state"`
			Items         []itemEntry `json:"items"`
			TotalPrice    float64     `json:"total_price"`
			TotalDiscount float64     `json:"total_discount,omitempty"`
		}

		items := make([]itemEntry, 0, len(order.Items))
		for _, it := range order.Items {
			ie := itemEntry{ProductID: it.ProductID, Quantity: it.Quantity}
			if it.Product != nil {
				ie.Name = it.Product.Title
				ie.Price = it.Product.Price.Now
			}
			items = append(items, ie)
		}
		return jsonResult(orderResult{
			ID:            order.ID,
			State:         order.State,
			Items:         items,
			TotalPrice:    order.TotalPrice,
			TotalDiscount: order.TotalDiscount,
		})
	})
}

// --- ah_get_cart ---

func registerGetCart(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_cart",
		mcp.WithTitleAnnotation("Albert Heijn: View Cart"),
		mcp.WithDescription(
			"View the current Albert Heijn online shopping cart (active order). "+
				"Returns the order state, all items with names and quantities, "+
				"total price, and total discount. "+
				"Use ah_update_cart_item or ah_remove_from_cart to modify items.",
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		order, err := c.GetOrder(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get cart: %v", err)), nil
		}

		type cartItem struct {
			ProductID int     `json:"product_id"`
			Name      string  `json:"name,omitempty"`
			Quantity  int     `json:"quantity"`
			Price     float64 `json:"price,omitempty"`
		}
		type cartResult struct {
			ID            string     `json:"id"`
			State         string     `json:"state"`
			Items         []cartItem `json:"items"`
			TotalPrice    float64    `json:"total_price"`
			TotalDiscount float64    `json:"total_discount,omitempty"`
		}
		items := make([]cartItem, 0, len(order.Items))
		for _, it := range order.Items {
			ci := cartItem{ProductID: it.ProductID, Quantity: it.Quantity}
			if it.Product != nil {
				ci.Name = it.Product.Title
				ci.Price = it.Product.Price.Now
			}
			items = append(items, ci)
		}
		return jsonResult(cartResult{
			ID:            order.ID,
			State:         order.State,
			Items:         items,
			TotalPrice:    order.TotalPrice,
			TotalDiscount: order.TotalDiscount,
		})
	})
}

// --- ah_get_cart_summary ---

func registerGetCartSummary(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_cart_summary",
		mcp.WithTitleAnnotation("Albert Heijn: Cart Summary"),
		mcp.WithDescription(
			"Get the Albert Heijn shopping cart totals: number of items, "+
				"total price, discount amount, and delivery cost.",
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		summary, err := c.GetOrderSummary(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get cart summary: %v", err)), nil
		}
		return jsonResult(summary)
	})
}

// --- ah_update_cart_item ---

func registerUpdateCartItem(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_update_cart_item",
		mcp.WithTitleAnnotation("Albert Heijn: Update Cart Item"),
		mcp.WithDescription(
			"Set the quantity of a product in the Albert Heijn shopping cart. "+
				"Use product_id from ah_search_products or ah_get_cart. "+
				"Set quantity=0 to remove the item (or use ah_remove_from_cart).",
		),
		mcp.WithString("product_id",
			mcp.Required(),
			mcp.Description("Numeric product ID"),
		),
		mcp.WithString("quantity",
			mcp.Required(),
			mcp.Description("New quantity (0 removes the item)"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		productID := req.GetInt("product_id", 0)
		if productID == 0 {
			return errResult("product_id is required"), nil
		}
		quantity := req.GetInt("quantity", -1)
		if quantity < 0 {
			return errResult("quantity is required and must be >= 0"), nil
		}

		// GetOrder must be called first so the client caches the active order ID,
		// which is sent as the Appie-Current-Order-Id header on write requests.
		if _, err := c.GetOrder(ctx); err != nil {
			return errResult(fmt.Sprintf("Failed to get active order: %v", err)), nil
		}

		if err := c.UpdateOrderItem(ctx, productID, quantity); err != nil {
			return errResult(fmt.Sprintf("Failed to update cart item %d: %v", productID, err)), nil
		}
		if quantity == 0 {
			return mcp.NewToolResultText(fmt.Sprintf("Product %d removed from cart.", productID)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Product %d quantity set to %d.", productID, quantity)), nil
	})
}

// --- ah_remove_from_cart ---

func registerRemoveFromCart(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_remove_from_cart",
		mcp.WithTitleAnnotation("Albert Heijn: Remove from Cart"),
		mcp.WithDescription(
			"Remove a single product from the Albert Heijn shopping cart. "+
				"Use product_id from ah_search_products or ah_get_cart.",
		),
		mcp.WithString("product_id",
			mcp.Required(),
			mcp.Description("Numeric product ID to remove"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		productID := req.GetInt("product_id", 0)
		if productID == 0 {
			return errResult("product_id is required"), nil
		}

		// GetOrder must be called first to cache the active order ID for the header.
		if _, err := c.GetOrder(ctx); err != nil {
			return errResult(fmt.Sprintf("Failed to get active order: %v", err)), nil
		}

		if err := c.RemoveFromOrder(ctx, productID); err != nil {
			return errResult(fmt.Sprintf("Failed to remove product %d from cart: %v", productID, err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Product %d removed from cart.", productID)), nil
	})
}

// --- ah_clear_cart ---

func registerClearCart(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_clear_cart",
		mcp.WithTitleAnnotation("Albert Heijn: Clear Cart"),
		mcp.WithDescription(
			"Remove ALL items from the Albert Heijn shopping cart. "+
				"Irreversible — requires confirm=\"yes\" to prevent accidental use.",
		),
		mcp.WithString("confirm",
			mcp.Required(),
			mcp.Description(`Must be "yes" to confirm clearing the entire cart`),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if req.GetString("confirm", "") != "yes" {
			return errResult(`confirm must be "yes" to clear the cart`), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		if err := c.ClearOrder(ctx); err != nil {
			return errResult(fmt.Sprintf("Failed to clear cart: %v", err)), nil
		}
		return mcp.NewToolResultText("Shopping cart cleared."), nil
	})
}

// --- ah_reopen_order ---

func registerReopenOrder(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_reopen_order",
		mcp.WithTitleAnnotation("Albert Heijn: Reopen Order for Editing"),
		mcp.WithDescription(
			"Unlock a submitted AH delivery order so its items can be changed. "+
				"The order becomes the active order again (REOPENED state). "+
				"IMPORTANT: You MUST call ah_revert_order when done, even if editing fails — "+
				"otherwise the order stays unlocked and interferes with new orders. "+
				"Only works before the order's closing time; returns an error if too late. "+
				"Get order_id from ah_get_order_history (check modifiable=true first).",
		),
		mcp.WithString("order_id",
			mcp.Required(),
			mcp.Description("Numeric order ID from ah_get_order_history"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		orderID := req.GetInt("order_id", 0)
		if orderID == 0 {
			return errResult("order_id is required"), nil
		}

		if err := c.ReopenOrder(ctx, orderID); err != nil {
			return errResult(fmt.Sprintf("Failed to reopen order %d: %v", orderID, err)), nil
		}
		// ReopenOrder changes server-side state; select it for the current-order header.
		c.SetOrderID(orderID)
		return mcp.NewToolResultText(fmt.Sprintf(
			"Order %d is now unlocked (REOPENED). Use ah_update_order_items to make changes, then call ah_revert_order when done.",
			orderID,
		)), nil
	})
}

// --- ah_update_order_items ---

func registerUpdateOrderItems(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_update_order_items",
		mcp.WithTitleAnnotation("Albert Heijn: Update Order Items"),
		mcp.WithDescription(
			"Add, update, or remove items from the currently active AH order. "+
				"Set quantity=0 to remove an item. "+
				"Use after ah_reopen_order to edit a submitted delivery order. "+
				"Returns confirmation of the update.",
		),
		mcp.WithArray("items",
			mcp.Required(),
			mcp.Description(`Items to add/update/remove. Each: {"product_id": 123456, "quantity": 2} — set quantity=0 to remove.`),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"product_id": map[string]any{"type": "integer"},
					"quantity":   map[string]any{"type": "integer"},
				},
				"required": []string{"product_id", "quantity"},
			}),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		rawItems, ok := req.GetArguments()["items"]
		if !ok {
			return errResult("items parameter is required"), nil
		}
		itemList, ok := rawItems.([]any)
		if !ok {
			return errResult("items must be an array"), nil
		}

		orderItems := make([]appie.OrderItem, 0, len(itemList))
		for _, raw := range itemList {
			m, mOK := raw.(map[string]any)
			if !mOK {
				continue
			}
			pid := toInt(m["product_id"])
			qty := toInt(m["quantity"])
			if pid > 0 {
				orderItems = append(orderItems, appie.OrderItem{ProductID: pid, Quantity: qty})
			}
		}
		if len(orderItems) == 0 {
			return errResult("no valid items provided"), nil
		}

		// GetOrder must be called first to cache the active order ID for the header.
		if _, err := c.GetOrder(ctx); err != nil {
			return errResult(fmt.Sprintf("Failed to get active order: %v", err)), nil
		}

		if err := c.AddToOrder(ctx, orderItems); err != nil {
			return errResult(fmt.Sprintf("Failed to update order items: %v", err)), nil
		}

		added, removed := 0, 0
		for _, it := range orderItems {
			if it.Quantity == 0 {
				removed++
			} else {
				added++
			}
		}
		return mcp.NewToolResultText(fmt.Sprintf(
			"Order updated: %d item(s) added/changed, %d item(s) removed. Call ah_revert_order to resubmit.",
			added, removed,
		)), nil
	})
}

// --- ah_revert_order ---

func registerRevertOrder(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_revert_order",
		mcp.WithTitleAnnotation("Albert Heijn: Resubmit Order"),
		mcp.WithDescription(
			"Resubmit a reopened AH delivery order back to its scheduled/submitted state. "+
				"ALWAYS call this after ah_reopen_order, whether editing succeeded or failed. "+
				"Clears the active order state on the client so the shopping cart works normally again.",
		),
		mcp.WithString("order_id",
			mcp.Required(),
			mcp.Description("Numeric order ID — same value passed to ah_reopen_order"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		orderID := req.GetInt("order_id", 0)
		if orderID == 0 {
			return errResult("order_id is required"), nil
		}

		c.SetOrderID(orderID)
		rollback := func() error {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return c.RevertOrder(rollbackCtx, orderID)
		}

		type orderMetadataResponse struct {
			Order struct {
				ID                 int    `json:"id"`
				State              string `json:"state"`
				LastUserChangeTime string `json:"lastUserChangeTime"`
			} `json:"order"`
		}
		const metadataQuery = `query ReopenedOrderMetadata { order { id state lastUserChangeTime } }`
		metadataCtx, cancelMetadata := context.WithTimeout(ctx, 20*time.Second)
		defer cancelMetadata()
		var metadata orderMetadataResponse
		if err := c.DoGraphQL(metadataCtx, metadataQuery, nil, &metadata); err != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return errResult(fmt.Sprintf("Failed to read reopened order metadata: %v; rollback also failed: %v", err, rollbackErr)), nil
			}
			return errResult(fmt.Sprintf("Failed to read reopened order metadata; changes were rolled back: %v", err)), nil
		}
		if metadata.Order.ID != orderID || metadata.Order.LastUserChangeTime == "" {
			if rollbackErr := rollback(); rollbackErr != nil {
				return errResult(fmt.Sprintf("Invalid reopened order metadata (id=%d, state=%s); rollback also failed: %v", metadata.Order.ID, metadata.Order.State, rollbackErr)), nil
			}
			return errResult(fmt.Sprintf("Invalid reopened order metadata; changes were rolled back (id=%d, state=%s)", metadata.Order.ID, metadata.Order.State)), nil
		}

		type confirmResponse struct {
			CheckoutConfirmOrder struct {
				Status       string `json:"status"`
				ErrorMessage string `json:"errorMessage"`
			} `json:"checkoutConfirmOrder"`
		}
		const confirmMutation = `mutation ConfirmReopenedOrder($orderId: Int!, $orderInfo: CheckoutConfirmOrderPayload!) {
  checkoutConfirmOrder(orderId: $orderId, orderInfo: $orderInfo) { status errorMessage }
}`
		variables := map[string]any{
			"orderId": orderID,
			"orderInfo": map[string]any{
				"channel":           "IOS",
				"orderLastModified": metadata.Order.LastUserChangeTime,
			},
		}
		confirmCtx, cancelConfirm := context.WithTimeout(ctx, 30*time.Second)
		defer cancelConfirm()
		var confirmed confirmResponse
		if err := c.DoGraphQL(confirmCtx, confirmMutation, variables, &confirmed); err != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return errResult(fmt.Sprintf("Failed to confirm reopened order: %v; rollback also failed: %v", err, rollbackErr)), nil
			}
			return errResult(fmt.Sprintf("Failed to confirm reopened order; changes were rolled back: %v", err)), nil
		}
		if confirmed.CheckoutConfirmOrder.Status != "SUCCESS" {
			if rollbackErr := rollback(); rollbackErr != nil {
				return errResult(fmt.Sprintf("Failed to confirm reopened order: %s; rollback also failed: %v", confirmed.CheckoutConfirmOrder.ErrorMessage, rollbackErr)), nil
			}
			return errResult(fmt.Sprintf("Failed to confirm reopened order; changes were rolled back: %s", confirmed.CheckoutConfirmOrder.ErrorMessage)), nil
		}
		if _, err := deps.ReloadClient(); err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Order %d was resubmitted with its changes, but the local client could not be reset: %v", orderID, err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf(
			"Order %d has been resubmitted with its changes. Your delivery is back on schedule.",
			orderID,
		)), nil
	})
}
