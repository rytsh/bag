class Runner
  include Billing::Helpers
  def go
    Billing::Processor.call
    p = Billing::Processor.new
    p.run
    x = Other.new
    x = Billing::Processor.new
    x.run
  end
end
