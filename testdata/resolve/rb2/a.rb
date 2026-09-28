module Billing
  module Helpers
    def fmt; end
  end
  class Processor
    include Helpers
    def self.call; end
    def run; end
  end
end
